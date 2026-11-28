// Command access-session-agent runs beside guacd in every session pod of the
// access plane (ADR-0033). It reads its configuration, posts ready to the
// access controller once guacd accepts a connection on the loopback address,
// relays the broker's tunnel to guacd after the handshake, ships the recording
// (none while chunkBytes is 0), and posts closed after the last chunk when the
// session ends: on SIGTERM, when guacd ends the connection, when a handshake
// fails, or when the recording fails. A tunnel the broker drops does not end
// the session; the broker may open the next connection.
//
// It reads no service-account token and opens no connection but to guacd on
// the loopback address, to the controller's internal port and on its own
// listener. It never logs a parameter value.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/config"
	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/handshake"
	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/relay"
	"github.com/openova-io/openova/core/cmd/access-session-agent/internal/shipper"
)

// exitConfig is the exit code of a configuration that is missing or not valid
// (EX_CONFIG).
const exitConfig = 78

// operator is the end reason the agent sends on SIGTERM: the controller ended
// the session and keeps the reason it chose.
const operator = "operator"

type options struct {
	config, guacd, rec, creds string
	listeners                 []net.Listener
	pause, giveUp, settle     time.Duration
	used                      func() (float64, error)
	log                       *slog.Logger
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	lns, err := podListeners("8443")
	if err != nil {
		log.Error("listen on 8443", "error", err)
		os.Exit(1)
	}
	code := run(ctx, options{config: config.Path, guacd: "127.0.0.1:4822", rec: handshake.RecPath, creds: "/var/run/secrets/openova",
		listeners: lns, pause: 500 * time.Millisecond, giveUp: 30 * time.Second, settle: 500 * time.Millisecond,
		used: shipper.VolumeUsed(handshake.RecPath), log: log})
	stop()
	os.Exit(code)
}

// podListeners listens on port of each address of the pod but loopback and
// link-local: the broker dials status.podIP; loopback is guacd's alone.
func podListeners(port string) ([]net.Listener, error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}
	var out []net.Listener
	for _, a := range addrs {
		ip, ok := a.(*net.IPNet)
		if !ok || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(ip.IP.String(), port))
		if err != nil {
			return nil, err
		}
		out = append(out, ln)
	}
	if len(out) == 0 {
		return nil, errors.New("the pod has no address but loopback")
	}
	return out, nil
}

func run(ctx context.Context, o options) int {
	cfg, err := config.Load(o.config)
	if err != nil {
		o.log.Error("the configuration cannot be used", "path", o.config, "error", err)
		return exitConfig
	}
	credentialID, err := handshake.CredentialID(cfg, o.creds)
	if err != nil {
		o.log.Error("no credential identifier", "kind", cfg.Kind, "error", err)
		return 1
	}
	client := &shipper.Client{Base: strings.TrimSuffix(cfg.Ingest, "/"), UID: cfg.SessionUID, HTTP: &http.Client{Timeout: 10 * time.Second}, Pause: o.pause}
	rec := &shipper.Recorder{Client: client, Dir: o.rec, ChunkBytes: cfg.ChunkBytes, ChunkTime: time.Duration(cfg.ChunkSeconds) * time.Second,
		Terminal: cfg.Terminal, GiveUp: o.giveUp, Settle: o.settle, Used: o.used}
	work, cancelWork := context.WithCancel(context.WithoutCancel(ctx)) // outlives SIGTERM until the last chunk
	defer cancelWork()

	ended := make(chan shipper.Failure, 4) // the first end the agent sees ends the session
	end := func(f shipper.Failure) {
		select {
		case ended <- f:
		default:
		}
	}
	var mu sync.Mutex
	var current *shipper.Connection
	dialer := &net.Dialer{Timeout: 2 * time.Second}
	srv := &relay.Server{Config: cfg, CredDir: o.creds, Now: time.Now, Log: o.log,
		Dial: func(ctx context.Context) (net.Conn, error) { return dialer.DialContext(ctx, "tcp", o.guacd) },
		Begin: func(n int) {
			if !cfg.Recording() {
				return
			}
			c, err := rec.Follow(work, n, end)
			if err != nil {
				end(shipper.Failure{Reason: shipper.RecordingFailed, Err: err})
				return
			}
			mu.Lock()
			current = c
			mu.Unlock()
		},
		Finish: func(n int, how relay.End, err error) {
			mu.Lock()
			c := current
			current = nil
			mu.Unlock()
			if c != nil {
				c.Stop()
			}
			switch {
			case how == relay.GuacdEnded:
				end(shipper.Failure{Reason: "user"})
			case how == relay.Refused && errors.Is(err, handshake.ErrCredentialExpired):
				end(shipper.Failure{Reason: "credential-expired", Err: err})
			case how == relay.Refused:
				end(shipper.Failure{Reason: "start-failed", Err: err})
			}
		},
	}
	hs := &http.Server{Handler: srv, ReadHeaderTimeout: 10 * time.Second}
	for _, ln := range o.listeners {
		go func() { _ = hs.Serve(ln) }()
	}
	go func() {
		if err := waitForGuacd(work, srv.Dial); err != nil {
			return
		}
		if err := client.Ready(work, credentialID); err != nil {
			o.log.Error("ready was not delivered", "error", err)
			return
		}
		o.log.Info("ready delivered", "credentialID", credentialID)
	}()

	code, reason := 0, operator
	select {
	case <-ctx.Done():
	case f := <-ended:
		reason = f.Reason
		if f.Reason == shipper.RecordingFailed || f.Reason == shipper.RecordingVolumeFull {
			code = 1
		}
		o.log.Info("the session ends", "reason", f.Reason, "error", f.Err)
	}
	srv.Close()
	_ = hs.Close()
	srv.Wait()
	closing, cancel := context.WithTimeout(work, 15*time.Second)
	defer cancel()
	if err := client.Closed(closing, reason); err != nil {
		o.log.Error("closed was not delivered", "reason", reason, "error", err)
		return 1
	}
	return code
}

// waitForGuacd returns once guacd accepts a connection.
func waitForGuacd(ctx context.Context, dial func(context.Context) (net.Conn, error)) error {
	for {
		if c, err := dial(ctx); err == nil {
			return c.Close()
		}
		t := time.NewTimer(200 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
}
