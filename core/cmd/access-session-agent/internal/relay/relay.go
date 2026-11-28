// Package relay is the agent's listener on port 8443 (ADR-0033): the tunnel
// from the broker (hop 2, a WebSocket on /tunnel) to guacd on the loopback
// address (hop 3, TCP), and /healthz.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/config"
	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/handshake"
)

// UIDHeader carries the UID of the session the broker opens a tunnel for.
const UIDHeader = "X-Access-Session-UID"

// End is how a connection ended.
type End string

const (
	// Dropped: the broker's socket ended first. The session stays; the broker
	// may open the next connection (reconnect rule).
	Dropped End = "dropped"
	// GuacdEnded: guacd ended the connection, so the protocol session is over.
	GuacdEnded End = "guacd-ended"
	// Refused: the handshake did not reach ready.
	Refused End = "refused"
	// Shutdown: Close ended it.
	Shutdown End = "shutdown"
)

const batchMax = 64 << 10

var timezoneRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_+-]{0,30}(/[A-Za-z0-9_+-]{1,30}){0,2}$`)

// Server serves /tunnel and /healthz. It takes one tunnel at a time and
// numbers the connections from 1.
type Server struct {
	Config  *config.Config
	CredDir string
	Dial    func(ctx context.Context) (net.Conn, error) // to guacd
	Now     func() time.Time
	Begin   func(n int)                     // before the handshake of connection n
	Finish  func(n int, how End, err error) // after connection n ended
	Log     *slog.Logger

	mu       sync.Mutex
	busy     bool
	closed   bool
	n        int
	active   []io.Closer
	upgrader websocket.Upgrader
	open     sync.WaitGroup // the tunnel being served, until its Finish returned
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/healthz":
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		c, err := s.Dial(ctx)
		if err != nil {
			fail(w, http.StatusServiceUnavailable, "guacd-unavailable", "guacd does not accept a connection")
			return
		}
		_ = c.Close()
		w.WriteHeader(http.StatusOK)
	case "/tunnel":
		s.tunnel(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *Server) tunnel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "method", "only GET opens a tunnel")
		return
	}
	if r.Header.Get(UIDHeader) != s.Config.SessionUID {
		fail(w, http.StatusForbidden, "uid-mismatch", "the tunnel is not for this session")
		return
	}
	size, err := parseSize(r)
	if err != nil {
		fail(w, http.StatusBadRequest, "bad-size", err.Error())
		return
	}
	n, code := s.claim()
	if code != "" {
		fail(w, http.StatusConflict, code, "the agent holds a tunnel already, or is shutting down")
		return
	}
	defer s.release()
	s.Begin(n)
	g, rd, err := s.handshake(r.Context(), n, size)
	if err != nil {
		s.Log.Warn("the handshake failed", "connection", n, "error", err)
		fail(w, http.StatusBadGateway, "handshake-failed", "guacd did not accept the connection")
		s.Finish(n, Refused, err)
		return
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		_ = g.Close()
		s.Finish(n, Dropped, err)
		return
	}
	if !s.track(ws, g) {
		_ = ws.Close()
		_ = g.Close()
		s.Finish(n, Shutdown, nil)
		return
	}
	s.Log.Info("tunnel open", "connection", n)
	how := pump(ws, g, rd)
	if s.isClosed() {
		how = Shutdown
	}
	s.Log.Info("tunnel ended", "connection", n, "how", string(how))
	s.Finish(n, how, nil)
}

// handshake dials guacd and runs the handshake of connection n. The
// parameter values are never logged.
func (s *Server) handshake(ctx context.Context, n int, size handshake.Size) (net.Conn, *handshake.Reader, error) {
	params, err := handshake.Params(s.Config, n, s.CredDir, s.Now())
	if err != nil {
		return nil, nil, err
	}
	g, err := s.Dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	rd := handshake.NewReader(g)
	if _, err := handshake.Run(g, rd, s.Config.Protocol, s.Config.Subject, size, params); err != nil {
		_ = g.Close()
		return nil, nil, err
	}
	return g, rd, nil
}

// pump copies guacd's instructions to the broker, whole instructions in one
// text message, and the broker's messages to guacd unchanged, until one side
// ends. The side that ended first says how the connection ended.
func pump(ws *websocket.Conn, g net.Conn, rd *handshake.Reader) End {
	ws.SetReadLimit(1 << 20)
	done := make(chan End, 2)
	go func() {
		var buf []byte
		for {
			raw, _, err := rd.Next(false)
			buf = append(buf, raw...)
			if len(buf) > 0 && (err != nil || rd.Buffered() == 0 || len(buf) >= batchMax) {
				if werr := ws.WriteMessage(websocket.TextMessage, buf); werr != nil {
					done <- Dropped
					return
				}
				buf = buf[:0]
			}
			if err != nil {
				done <- GuacdEnded
				return
			}
		}
	}()
	go func() {
		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				done <- Dropped
				return
			}
			if _, err := g.Write(msg); err != nil {
				done <- GuacdEnded
				return
			}
		}
	}()
	how := <-done
	if how == GuacdEnded {
		_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	}
	_ = ws.Close()
	_ = g.Close()
	<-done
	return how
}

func parseSize(r *http.Request) (handshake.Size, error) {
	q := r.URL.Query()
	num := func(k string, max int) (int, error) {
		v, err := strconv.Atoi(q.Get(k))
		if err != nil || v < 1 || v > max {
			return 0, errors.New("query " + k + " is not an integer from 1 to " + strconv.Itoa(max))
		}
		return v, nil
	}
	var s handshake.Size
	var err error
	if s.Width, err = num("width", 16384); err != nil {
		return s, err
	}
	if s.Height, err = num("height", 16384); err != nil {
		return s, err
	}
	if s.DPI, err = num("dpi", 2400); err != nil {
		return s, err
	}
	if s.Timezone = q.Get("timezone"); !timezoneRE.MatchString(s.Timezone) {
		return s, errors.New("query timezone is not a time zone name")
	}
	return s, nil
}

func (s *Server) claim() (int, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.closed:
		return 0, "shutting-down"
	case s.busy:
		return 0, "busy"
	}
	s.busy = true
	s.open.Add(1)
	s.n++
	return s.n, ""
}

func (s *Server) release() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.busy, s.active = false, nil
	s.open.Done()
}

// Wait returns when no tunnel is open. After Close no tunnel opens.
func (s *Server) Wait() { s.open.Wait() }

func (s *Server) track(c ...io.Closer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active = c
	return !s.closed
}

func (s *Server) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

// Close takes no new tunnel and ends the open one.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, c := range s.active {
		_ = c.Close()
	}
}

func fail(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code, "message": message})
}
