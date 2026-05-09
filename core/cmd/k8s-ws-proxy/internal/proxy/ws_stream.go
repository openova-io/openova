package proxy

import (
	"errors"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// wsStream adapts a gorilla/websocket connection into io.Reader /
// io.Writer pairs that the client-go remotecommand executor can plug
// into its Stdin/Stdout/Stderr fields.
//
// The k8s exec channel protocol multiplexes stdin/stdout/stderr/error
// onto a single binary WebSocket using a one-byte channel prefix per
// frame (0=stdin, 1=stdout, 2=stderr, 3=error, 4=resize). The
// remotecommand SDK already speaks this channel format on the
// kube-apiserver side via SPDY; on the browser side, terminal
// emulators (xterm.js + the @kubernetes/client-node helpers) do the
// same.
//
// The proxy treats the WebSocket as the channelled side: it reads
// channelled frames from the browser and routes them to the SDK's
// Stdin pipe; it tags writes from Stdout / Stderr with their channel
// byte and emits them as binary frames.
//
// This adapter is "good enough" for the EPIC-4 K1 contract: the test
// harness (httptest.Server + a mock kube-apiserver in the integration
// test) uses these streams unmodified.
type wsStream struct {
	conn *websocket.Conn

	// writer mutex — gorilla/websocket forbids concurrent WriteMessage
	// calls. Stdout + Stderr both serialize through writeMu.
	writeMu sync.Mutex

	// channels — Stdin is fed from the WebSocket reader goroutine;
	// Stdout / Stderr are buffered into writers that frame each
	// chunk as a channelled binary message back to the WS.
	stdinR io.Reader
	stdinW io.WriteCloser

	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer

	// pinger drives WebSocket keepalive pings so a half-closed
	// connection (e.g. client browser tab killed) is detected
	// quickly enough to release the apiserver-side exec stream.
	pingPeriod time.Duration
	stop       chan struct{}
	stopOnce   sync.Once
}

// newWSStream wires the read+write goroutines and returns a stream
// the SDK can consume directly.
func newWSStream(conn *websocket.Conn, pingPeriod time.Duration) *wsStream {
	stdinR, stdinW := io.Pipe()
	s := &wsStream{
		conn:       conn,
		stdinR:     stdinR,
		stdinW:     stdinW,
		pingPeriod: pingPeriod,
		stop:       make(chan struct{}),
	}
	s.Stdin = stdinR
	s.Stdout = &channelWriter{ws: s, channel: 1}
	s.Stderr = &channelWriter{ws: s, channel: 2}

	go s.readLoop()
	if pingPeriod > 0 {
		go s.pingLoop()
	}
	return s
}

// Close ends the read/ping loops and releases the stdin pipe so the
// SDK's exec Reader unblocks promptly.
func (s *wsStream) Close() {
	s.stopOnce.Do(func() {
		close(s.stop)
		_ = s.stdinW.Close()
	})
}

// readLoop pulls messages off the WebSocket and routes them onto the
// stdin pipe. Channel-byte aware: a message starting with 0x00 is
// stdin payload (apiserver side expects raw bytes on its Stdin
// reader); messages on other channels (resize, etc.) are dropped for
// the K1 first cut — full channel-routing comes in EPIC-4 E2.
func (s *wsStream) readLoop() {
	defer s.Close()
	for {
		select {
		case <-s.stop:
			return
		default:
		}
		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			return
		}
		if len(msg) == 0 {
			continue
		}
		channel := msg[0]
		payload := msg[1:]
		switch channel {
		case 0: // stdin
			if _, err := s.stdinW.Write(payload); err != nil {
				return
			}
		case 4: // resize — TODO E2: forward to remotecommand TerminalSizeQueue
		default:
			// Unknown channel; ignore. Browsers MUST NOT send beyond
			// the documented channels; if they do, dropping is
			// preferable to abort.
		}
	}
}

// pingLoop emits a ping every pingPeriod. gorilla/websocket replies
// with pong automatically, but a missing pong manifests as a write
// error on the next ping which closes the loop and unwinds the bridge.
func (s *wsStream) pingLoop() {
	t := time.NewTicker(s.pingPeriod)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.writeMu.Lock()
			err := s.conn.WriteControl(
				websocket.PingMessage,
				nil,
				time.Now().Add(5*time.Second),
			)
			s.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// channelWriter prefixes every write with its channel byte and emits
// a binary message. WriteMessage is serialized via the parent's
// writeMu — concurrent calls are safe.
type channelWriter struct {
	ws      *wsStream
	channel byte
}

func (c *channelWriter) Write(p []byte) (int, error) {
	if c.ws == nil || c.ws.conn == nil {
		return 0, errors.New("ws stream closed")
	}
	frame := make([]byte, 1+len(p))
	frame[0] = c.channel
	copy(frame[1:], p)
	c.ws.writeMu.Lock()
	defer c.ws.writeMu.Unlock()
	if err := c.ws.conn.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		return 0, err
	}
	return len(p), nil
}
