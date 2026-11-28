package shipper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// End reasons of a failed recording (the session's endReason).
const (
	RecordingFailed     = "recording-failed"
	RecordingVolumeFull = "recording-volume-full"
)

// Failure ends the session: the recording cannot go on.
type Failure struct {
	Reason string
	Err    error
}

// Recorder ships the recording files that guacd writes for each connection:
// screen-<n>, and with a terminal typescript-<n> and typescript-<n>.timing,
// as the streams screen-<n>, typescript-<n> and typescript-timing-<n>. A chunk
// is sealed when ChunkBytes are unsent, when ChunkTime passed since the first
// unsent byte was read, and when the connection ends.
type Recorder struct {
	Client     *Client
	Dir        string
	ChunkBytes int
	ChunkTime  time.Duration
	Terminal   bool
	GiveUp     time.Duration           // a chunk not delivered for this long fails the recording (30 s)
	Settle     time.Duration           // at the end, the files are read until they did not grow for this long
	Used       func() (float64, error) // the share of the recording volume in use; 0.9 or more fails it
}

// Connection ships the files of one connection until Stop.
type Connection struct {
	watch   io.Closer
	streams []*stream
	wg      sync.WaitGroup
}

type stream struct {
	r      *Recorder
	name   string
	path   string
	f      *os.File
	seq    int
	wake   chan struct{}
	stop   chan struct{}
	failed func(Failure)
}

// Follow starts shipping the files of connection n. A failure is reported
// once to failed, and that stream ships nothing more.
func (r *Recorder) Follow(ctx context.Context, n int, failed func(Failure)) (*Connection, error) {
	c := &Connection{}
	names := [][2]string{{"screen-" + strconv.Itoa(n), "screen-" + strconv.Itoa(n)}}
	if r.Terminal {
		names = append(names, [2]string{"typescript-" + strconv.Itoa(n), "typescript-" + strconv.Itoa(n)},
			[2]string{"typescript-timing-" + strconv.Itoa(n), "typescript-" + strconv.Itoa(n) + ".timing"})
	}
	for _, nm := range names {
		c.streams = append(c.streams, &stream{r: r, name: nm[0], path: filepath.Join(r.Dir, nm[1]),
			wake: make(chan struct{}, 1), stop: make(chan struct{}), failed: failed})
	}
	var once sync.Once
	var last time.Time
	w, err := watch(r.Dir, func() {
		for _, s := range c.streams {
			select {
			case s.wake <- struct{}{}:
			default:
			}
		}
		if time.Since(last) < time.Second {
			return
		}
		last = time.Now()
		if used, err := r.Used(); err != nil || used >= 0.9 {
			once.Do(func() {
				failed(Failure{RecordingVolumeFull, fmt.Errorf("the recording volume is %.0f%% full (%v)", 100*used, err)})
			})
		}
	})
	if err != nil {
		return nil, err
	}
	c.watch = w
	for _, s := range c.streams {
		c.wg.Add(1)
		go func() { defer c.wg.Done(); s.run(ctx) }()
	}
	return c, nil
}

// Stop seals and ships what is left of every file, then returns.
func (c *Connection) Stop() {
	for _, s := range c.streams {
		close(s.stop)
	}
	c.wg.Wait()
	_ = c.watch.Close()
	for _, s := range c.streams {
		if s.f != nil {
			_ = s.f.Close()
		}
	}
}

func (s *stream) run(ctx context.Context) {
	var pending []byte
	var first time.Time
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	for stopping := false; ; {
		select {
		case <-s.wake:
		case <-timer.C:
		case <-s.stop:
			stopping = true
			s.settle()
		}
		b, err := s.read()
		if err != nil {
			s.failed(Failure{RecordingFailed, err})
			return
		}
		if len(b) > 0 && len(pending) == 0 {
			first = time.Now()
			timer.Reset(s.r.ChunkTime)
		}
		pending = append(pending, b...)
		for len(pending) >= s.r.ChunkBytes || (len(pending) > 0 && (stopping || time.Since(first) >= s.r.ChunkTime)) {
			k := min(len(pending), s.r.ChunkBytes)
			s.seq++
			if err := s.r.Client.Chunk(ctx, s.name, s.seq, pending[:k], s.r.GiveUp); err != nil {
				s.failed(Failure{RecordingFailed, fmt.Errorf("chunk %d of stream %s: %w", s.seq, s.name, err)})
				return
			}
			if pending = pending[k:]; len(pending) > 0 {
				first = time.Now()
				timer.Reset(s.r.ChunkTime)
			}
		}
		if stopping {
			return
		}
	}
}

// settle waits until the file did not change for Settle, at most 8 times as long.
func (s *stream) settle() {
	limit := time.NewTimer(8 * s.r.Settle)
	defer limit.Stop()
	for {
		quiet := time.NewTimer(s.r.Settle)
		select {
		case <-s.wake:
			quiet.Stop()
		case <-quiet.C:
			return
		case <-limit.C:
			quiet.Stop()
			return
		}
	}
}

// read returns the bytes the file gained since the last read; nothing while
// guacd has not created it.
func (s *stream) read() ([]byte, error) {
	if s.f == nil {
		f, err := os.Open(filepath.Clean(s.path))
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		s.f = f
	}
	return io.ReadAll(s.f)
}

// watch calls wake on every change of a file in dir (inotify, no timer).
func watch(dir string, wake func()) (io.Closer, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("inotify: %w", err)
	}
	if _, err := syscall.InotifyAddWatch(fd, dir, syscall.IN_CREATE|syscall.IN_MODIFY|syscall.IN_CLOSE_WRITE|syscall.IN_MOVED_TO); err != nil {
		_ = syscall.Close(fd)
		return nil, fmt.Errorf("watch %s: %w", dir, err)
	}
	f := os.NewFile(uintptr(fd), "inotify")
	go func() {
		buf := make([]byte, 16<<10)
		for {
			if _, err := f.Read(buf); err != nil {
				return
			}
			wake()
		}
	}()
	return f, nil
}

// VolumeUsed is the share of the file system of dir in use, by statfs.
func VolumeUsed(dir string) func() (float64, error) {
	return func() (float64, error) {
		var st syscall.Statfs_t
		if err := syscall.Statfs(dir, &st); err != nil {
			return 0, err
		}
		if st.Blocks == 0 {
			return 0, errors.New("the file system reports no blocks")
		}
		return float64(st.Blocks-st.Bfree) / float64(st.Blocks), nil
	}
}
