// Command agent is the throwaway relay and test driver of lab test ACC-020.
// It lives only on the branch spike/ACC-020 and is never merged.
//
// Two roles, one binary:
//
//	agent serve   The session agent: accepts one WebSocket tunnel at a time on
//	              :8443 (subprotocol "guacamole"), performs the guacd handshake
//	              (select, args, size, audio, video, image, timezone, name,
//	              connect, ready) against 127.0.0.1:4822 and then relays
//	              instructions both ways. Because the spike has one hop, it also
//	              plays the broker's browser-facing part (the internal
//	              instructions of the browser library); query switches turn
//	              each of those parts off so their necessity can be measured.
//	              It also serves the test pages and the recording files.
//	agent drive   A scripted Guacamole client that connects to the tunnel from
//	              inside the pod and measures what the lab test asks for.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/gorilla/websocket"
)

const (
	configPath = "/etc/access/config.json"
	credDir    = "/var/run/secrets/openova"
	recDir     = "/rec"
	webDir     = "/web"
	guacdAddr  = "127.0.0.1:4822"
	listenAddr = ":8443"
)

// ------------------------------------------------------------------ protocol

type instr struct {
	Op   string
	Args []string
	Raw  string
}

func elem(b *strings.Builder, s string) {
	b.WriteString(strconv.Itoa(utf8.RuneCountInString(s)))
	b.WriteByte('.')
	b.WriteString(s)
}

func encode(op string, args ...string) string {
	var b strings.Builder
	elem(&b, op)
	for _, a := range args {
		b.WriteByte(',')
		elem(&b, a)
	}
	b.WriteByte(';')
	return b.String()
}

type parser struct{ r *bufio.Reader }

func newParser(r io.Reader) *parser { return &parser{bufio.NewReaderSize(r, 1<<16)} }

// next returns one complete instruction. Lengths count Unicode code points,
// as the Guacamole protocol defines them.
func (p *parser) next() (instr, error) {
	var raw strings.Builder
	var el []string
	for {
		ls, err := p.r.ReadString('.')
		if err != nil {
			if err == io.EOF && ls == "" && raw.Len() == 0 {
				return instr{}, io.EOF
			}
			if err == io.EOF {
				return instr{}, io.ErrUnexpectedEOF
			}
			return instr{}, err
		}
		raw.WriteString(ls)
		n, err := strconv.Atoi(ls[:len(ls)-1])
		if err != nil || n < 0 {
			return instr{}, fmt.Errorf("bad element length %q", trunc(ls, 40))
		}
		var v strings.Builder
		for i := 0; i < n; i++ {
			r, _, err := p.r.ReadRune()
			if err != nil {
				return instr{}, io.ErrUnexpectedEOF
			}
			v.WriteRune(r)
		}
		raw.WriteString(v.String())
		el = append(el, v.String())
		t, err := p.r.ReadByte()
		if err != nil {
			return instr{}, io.ErrUnexpectedEOF
		}
		raw.WriteByte(t)
		if t == ';' {
			return instr{Op: el[0], Args: el[1:], Raw: raw.String()}, nil
		}
		if t != ',' {
			return instr{}, fmt.Errorf("bad terminator %q", t)
		}
	}
}

func parseAll(s string) ([]instr, error) {
	p := newParser(strings.NewReader(s))
	var out []instr
	for {
		in, err := p.next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, in)
	}
}

// ------------------------------------------------------------------ helpers

var (
	logMu  sync.Mutex
	logOut io.Writer = os.Stdout
)

func logj(ev string, kv map[string]any) {
	if kv == nil {
		kv = map[string]any{}
	}
	kv["ev"] = ev
	kv["at"] = time.Now().UTC().Format("15:04:05.000")
	b, _ := json.Marshal(kv)
	logMu.Lock()
	fmt.Fprintln(logOut, "SPIKE "+string(b))
	logMu.Unlock()
}

func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("…(+%d bytes)", len(s)-n)
}

func errStr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// shortVal keeps credentials out of the log while still showing what kind of
// value was passed: secrets show their length, long values their first line
// (a PEM header or a key type) and a short digest.
func shortVal(name, v string) string {
	if v == "" {
		return ""
	}
	if name == "password" || name == "passphrase" {
		return fmt.Sprintf("<secret, %d chars>", len(v))
	}
	if len(v) > 64 || strings.Contains(v, "\n") {
		first := strings.SplitN(v, "\n", 2)[0]
		if len(first) > 44 {
			first = first[:44]
		}
		sum := sha256.Sum256([]byte(v))
		return fmt.Sprintf("<%d bytes, first line %q, sha256 %s…>", len(v), first, hex.EncodeToString(sum[:6]))
	}
	return v
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func def(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func printable(b []byte, n int) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == '\n':
			sb.WriteString("⏎")
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		}
	}
	s := sb.String()
	if len(s) > n {
		s = s[len(s)-n:]
	}
	return s
}

// ------------------------------------------------------------------ config

type scenario struct {
	Protocol   string            `json:"protocol"`
	Params     map[string]string `json:"params"`
	ParamFiles map[string]string `json:"paramFiles"`
	Record     bool              `json:"record"`
	Terminal   bool              `json:"terminal"`
}

type agentConfig struct {
	Subject   string              `json:"subject"`
	Scenarios map[string]scenario `json:"scenarios"`
}

func loadConfig() (agentConfig, error) {
	var c agentConfig
	b, err := os.ReadFile(configPath)
	if err != nil {
		return c, err
	}
	return c, json.Unmarshal(b, &c)
}

// buildParams assembles the guacd parameter map the way the design record
// prescribes: the common recording, typescript and clipboard parameters,
// then the scenario's literal parameters, then the credential files read at
// handshake time, then spike-only overrides ("name:value"; "-" deletes).
func buildParams(cfg agentConfig, name, run string, sets []string) (scenario, map[string]string, error) {
	sc, ok := cfg.Scenarios[name]
	if !ok {
		return sc, nil, fmt.Errorf("unknown scenario %q", name)
	}
	p := map[string]string{"disable-copy": "true", "disable-paste": "true"}
	if sc.Record {
		p["recording-path"] = recDir
		p["recording-name"] = "screen-" + run
		p["create-recording-path"] = "true"
		p["recording-include-keys"] = "false"
		if sc.Terminal {
			p["typescript-path"] = recDir
			p["typescript-name"] = "typescript-" + run
			p["create-typescript-path"] = "true"
		}
	}
	for k, v := range sc.Params {
		p[k] = v
	}
	for k, f := range sc.ParamFiles {
		b, err := os.ReadFile(filepath.Join(credDir, f))
		if err != nil {
			return sc, nil, fmt.Errorf("parameter file for %s: %w", k, err)
		}
		p[k] = string(b)
	}
	for _, s := range sets {
		k, v, _ := strings.Cut(s, ":")
		if v == "-" {
			delete(p, k)
		} else {
			p[k] = v
		}
	}
	return sc, p, nil
}

// ------------------------------------------------------------------ handshake

type hsResult struct {
	Args     []string
	ReadyID  string
	Pre      []instr
	Sent     []string
	Received []string
	Unknown  []string
	Ms       int64
}

func handshake(conn net.Conn, p *parser, protocol string, params map[string]string, w, h, dpi, tz, subject string) (hsResult, error) {
	var res hsResult
	t0 := time.Now()
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer func() { _ = conn.SetDeadline(time.Time{}) }()
	send := func(wire, logged string) error {
		res.Sent = append(res.Sent, logged)
		_, err := io.WriteString(conn, wire)
		return err
	}
	sel := encode("select", protocol)
	if err := send(sel, sel); err != nil {
		return res, err
	}
	a, err := p.next()
	if err != nil {
		return res, fmt.Errorf("reading args: %w", err)
	}
	res.Received = append(res.Received, a.Raw)
	if a.Op != "args" {
		return res, fmt.Errorf("expected args, got %s", trunc(a.Raw, 200))
	}
	res.Args = a.Args
	for _, s := range []string{
		encode("size", w, h, dpi),
		encode("audio"),
		encode("video"),
		encode("image", "image/png", "image/jpeg", "image/webp"),
		encode("timezone", tz),
		encode("name", subject),
	} {
		if err := send(s, s); err != nil {
			return res, err
		}
	}
	known := map[string]bool{}
	values := make([]string, 0, len(a.Args))
	logged := make([]string, 0, len(a.Args))
	for i, n := range a.Args {
		if i == 0 && strings.HasPrefix(n, "VERSION_") {
			values = append(values, n)
			logged = append(logged, n)
			continue
		}
		known[n] = true
		v := params[n]
		values = append(values, v)
		logged = append(logged, shortVal(n, v))
	}
	for k := range params {
		if !known[k] {
			res.Unknown = append(res.Unknown, k)
		}
	}
	sort.Strings(res.Unknown)
	if err := send(encode("connect", values...), encode("connect", logged...)); err != nil {
		return res, err
	}
	for {
		in, err := p.next()
		if err != nil {
			return res, fmt.Errorf("waiting for ready: %w", err)
		}
		res.Received = append(res.Received, trunc(in.Raw, 300))
		switch in.Op {
		case "ready":
			res.ReadyID = strings.Join(in.Args, ",")
			res.Ms = time.Since(t0).Milliseconds()
			return res, nil
		case "error":
			return res, fmt.Errorf("guacd error before ready: %q", in.Args)
		default:
			res.Pre = append(res.Pre, in)
		}
	}
}

// ------------------------------------------------------------------ serve

type server struct{ busy atomic.Bool }

var (
	upWith = websocket.Upgrader{
		CheckOrigin:     func(*http.Request) bool { return true },
		Subprotocols:    []string{"guacamole"},
		ReadBufferSize:  1 << 16,
		WriteBufferSize: 1 << 16,
	}
	upWithout = websocket.Upgrader{
		CheckOrigin:     func(*http.Request) bool { return true },
		ReadBufferSize:  1 << 16,
		WriteBufferSize: 1 << 16,
	}
)

type counter struct {
	mu sync.Mutex
	m  map[string]int
}

func (c *counter) add(op string) {
	if op == "" {
		op = "(internal)"
	}
	c.mu.Lock()
	c.m[op]++
	c.mu.Unlock()
}

func (c *counter) snapshot() map[string]int {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]int, len(c.m))
	for k, v := range c.m {
		out[k] = v
	}
	return out
}

func (s *server) tunnel(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if !s.busy.CompareAndSwap(false, true) {
		http.Error(w, `{"error":"busy"}`, http.StatusConflict)
		return
	}
	defer s.busy.Store(false)
	up := upWith
	if q.Get("subproto") == "0" {
		up = upWithout
	}
	requested := websocket.Subprotocols(r)
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		logj("tunnel-upgrade-failed", map[string]any{"err": err.Error(), "requestedSubprotocols": requested})
		return
	}
	defer ws.Close()
	scName := q.Get("scenario")
	run := def(q.Get("run"), scName)
	echoPing := q.Get("echoping") != "0"
	uuidFrame := q.Get("uuidframe") != "0"
	fwdInternal := q.Get("fwdinternal") == "1"
	framing := def(q.Get("framing"), "aligned")
	started := time.Now()
	logj("tunnel-open", map[string]any{
		"scenario": scName, "run": run, "requestedSubprotocols": requested, "chosenSubprotocol": ws.Subprotocol(),
		"uuidframe": uuidFrame, "echoping": echoPing, "fwdinternal": fwdInternal, "framing": framing,
		"set": q["set"], "remote": r.RemoteAddr,
	})

	var wmu sync.Mutex
	write := func(s string) error {
		wmu.Lock()
		defer wmu.Unlock()
		return ws.WriteMessage(websocket.TextMessage, []byte(s))
	}
	fail := func(stage string, err error) {
		logj("tunnel-error", map[string]any{"run": run, "stage": stage, "err": errStr(err)})
		_ = write(encode("error", err.Error(), "519"))
		time.Sleep(300 * time.Millisecond)
	}

	cfg, err := loadConfig()
	if err != nil {
		fail("config", err)
		return
	}
	sc, params, err := buildParams(cfg, scName, run, q["set"])
	if err != nil {
		fail("params", err)
		return
	}
	conn, err := net.DialTimeout("tcp", guacdAddr, 5*time.Second)
	if err != nil {
		fail("dial-guacd", err)
		return
	}
	defer conn.Close()
	p := newParser(conn)
	hs, err := handshake(conn, p, sc.Protocol, params,
		def(q.Get("width"), "1024"), def(q.Get("height"), "768"), def(q.Get("dpi"), "96"),
		def(q.Get("timezone"), "UTC"), cfg.Subject)
	logj("handshake", map[string]any{
		"run": run, "scenario": scName, "protocol": sc.Protocol, "args": hs.Args, "sent": hs.Sent,
		"received": hs.Received, "readyID": hs.ReadyID, "ms": hs.Ms, "err": errStr(err),
		"paramsNotInArgs": hs.Unknown, "instructionsBeforeReady": len(hs.Pre),
	})
	if err != nil {
		fail("handshake", err)
		return
	}

	sendInstr := func(raw string) error {
		if framing == "split" && utf8.RuneCountInString(raw) > 4 {
			rs := []rune(raw)
			mid := len(rs) / 2
			if err := write(string(rs[:mid])); err != nil {
				return err
			}
			return write(string(rs[mid:]))
		}
		return write(raw)
	}
	if uuidFrame {
		f := encode("", newUUID())
		_ = write(f)
		logj("server-first-frame", map[string]any{"run": run, "raw": f})
	}

	fromGuacd := &counter{m: map[string]int{}}
	fromBrowser := &counter{m: map[string]int{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for _, in := range hs.Pre {
			fromGuacd.add(in.Op)
			_ = sendInstr(in.Raw)
		}
		first := true
		for {
			in, err := p.next()
			if err != nil {
				logj("guacd-stream-ended", map[string]any{"run": run, "err": errStr(err)})
				_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
				_ = ws.Close()
				return
			}
			fromGuacd.add(in.Op)
			if first {
				logj("guacd-first-after-ready", map[string]any{"run": run, "raw": trunc(in.Raw, 160)})
				first = false
			}
			if in.Op == "error" || in.Op == "disconnect" || in.Op == "log" {
				logj("guacd-"+in.Op, map[string]any{"run": run, "raw": trunc(in.Raw, 300)})
			}
			if err := sendInstr(in.Raw); err != nil {
				return
			}
		}
	}()

	nFrames, nInternal, nEcho := 0, 0, 0
loop:
	for {
		_, msg, err := ws.ReadMessage()
		if err != nil {
			logj("browser-stream-ended", map[string]any{"run": run, "err": errStr(err)})
			break
		}
		nFrames++
		if nFrames <= 12 {
			logj("browser-frame", map[string]any{"run": run, "n": nFrames, "raw": trunc(string(msg), 160)})
		}
		ins, perr := parseAll(string(msg))
		if perr != nil {
			logj("browser-parse-error", map[string]any{"run": run, "err": perr.Error(), "raw": trunc(string(msg), 160)})
		}
		for _, in := range ins {
			fromBrowser.add(in.Op)
			if in.Op == "" {
				nInternal++
				if nInternal <= 3 {
					logj("browser-internal", map[string]any{"run": run, "raw": in.Raw})
				}
				if echoPing && len(in.Args) >= 2 && in.Args[0] == "ping" {
					e := encode("", "ping", in.Args[1])
					_ = write(e)
					nEcho++
					if nEcho <= 2 {
						logj("server-ping-echo", map[string]any{"run": run, "raw": e})
					}
				}
				if fwdInternal {
					if _, err := io.WriteString(conn, in.Raw); err != nil {
						break loop
					}
				}
				continue
			}
			if _, err := io.WriteString(conn, in.Raw); err != nil {
				logj("guacd-write-failed", map[string]any{"run": run, "err": err.Error()})
				break loop
			}
		}
	}
	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	logj("tunnel-closed", map[string]any{
		"run": run, "seconds": time.Since(started).Seconds(), "fromGuacd": fromGuacd.snapshot(),
		"fromBrowser": fromBrowser.snapshot(), "pingEchoes": nEcho,
	})
}

func safeRecPath(name string) (string, bool) {
	if name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return "", false
	}
	return filepath.Join(recDir, name), true
}

// serveRec serves a recording file. With piece and delay it streams the file
// in pieces of that many bytes, flushing each and waiting between them, so the
// client sees it arrive the way a chunked store would deliver it. With offset
// and length it serves one slice, the way one stored chunk object would be.
func serveRec(w http.ResponseWriter, r *http.Request) {
	path, ok := safeRecPath(strings.TrimPrefix(r.URL.Path, "/rec/"))
	if !ok {
		http.Error(w, "bad name", http.StatusBadRequest)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer f.Close()
	q := r.URL.Query()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	if q.Get("offset") != "" {
		off, _ := strconv.ParseInt(q.Get("offset"), 10, 64)
		n, _ := strconv.ParseInt(q.Get("length"), 10, 64)
		if _, err := f.Seek(off, io.SeekStart); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		_, _ = io.Copy(w, io.LimitReader(f, n))
		return
	}
	piece, _ := strconv.Atoi(q.Get("piece"))
	delay, _ := strconv.Atoi(q.Get("delay"))
	if piece <= 0 {
		_, _ = io.Copy(w, f)
		return
	}
	fl, _ := w.(http.Flusher)
	buf := make([]byte, piece)
	pieces := 0
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			pieces++
			if fl != nil {
				fl.Flush()
			}
			if delay > 0 {
				time.Sleep(time.Duration(delay) * time.Millisecond)
			}
		}
		if err != nil {
			break
		}
	}
	logj("rec-served-in-pieces", map[string]any{"file": filepath.Base(path), "piece": piece, "pieces": pieces})
}

func listRec(w http.ResponseWriter, _ *http.Request) {
	ents, _ := os.ReadDir(recDir)
	out := map[string]any{}
	for _, e := range ents {
		fi, err := e.Info()
		if err != nil {
			continue
		}
		out[e.Name()] = map[string]any{"bytes": fi.Size(), "mode": fi.Mode().String()}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func runServe() int {
	s := &server{}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		c, err := net.DialTimeout("tcp", guacdAddr, 300*time.Millisecond)
		if err != nil {
			http.Error(w, "guacd not listening", http.StatusServiceUnavailable)
			return
		}
		_ = c.Close()
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/tunnel", s.tunnel)
	mux.HandleFunc("/rec/", serveRec)
	mux.HandleFunc("/reclist", listRec)
	mux.Handle("/", http.FileServer(http.Dir(webDir)))
	logj("agent-listening", map[string]any{"addr": listenAddr})
	srv := &http.Server{Addr: listenAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		logj("agent-exit", map[string]any{"err": err.Error()})
		return 1
	}
	return 0
}

// ------------------------------------------------------------------ drive

type clipRec struct {
	Ms   int64  `json:"ms"`
	Mime string `json:"mime"`
	Text string `json:"text"`
}

type clipBuf struct {
	mime string
	data []byte
}

type driver struct {
	ws       *websocket.Conn
	wmu      sync.Mutex
	mu       sync.Mutex
	t0       time.Time
	counts   map[string]int
	streams  map[string]*clipBuf
	clips    []clipRec
	acks     []string
	errs     []string
	firstOps []string
	closed   chan struct{}
	closeErr string
	uuid     string
	width    int
	height   int
	firstAt  time.Duration
}

func (d *driver) send(op string, args ...string) {
	d.wmu.Lock()
	defer d.wmu.Unlock()
	_ = d.ws.WriteMessage(websocket.TextMessage, []byte(encode(op, args...)))
}

func (d *driver) readLoop() {
	defer close(d.closed)
	for {
		_, msg, err := d.ws.ReadMessage()
		if err != nil {
			d.mu.Lock()
			d.closeErr = err.Error()
			d.mu.Unlock()
			return
		}
		ins, _ := parseAll(string(msg))
		for _, in := range ins {
			d.handle(in)
		}
	}
}

func (d *driver) handle(in instr) {
	d.mu.Lock()
	defer d.mu.Unlock()
	op := in.Op
	if op == "" {
		op = "(internal)"
	} else if d.firstAt == 0 {
		d.firstAt = time.Since(d.t0)
	}
	d.counts[op]++
	if len(d.firstOps) < 12 {
		d.firstOps = append(d.firstOps, trunc(in.Raw, 90))
	}
	switch in.Op {
	case "":
		if len(in.Args) == 1 && d.uuid == "" {
			d.uuid = in.Args[0]
		}
	case "sync":
		if len(in.Args) > 0 {
			d.send("sync", in.Args[0])
		}
	case "clipboard":
		if len(in.Args) >= 2 {
			d.streams[in.Args[0]] = &clipBuf{mime: in.Args[1]}
		}
	case "blob":
		if len(in.Args) >= 2 {
			if c := d.streams[in.Args[0]]; c != nil {
				b, _ := base64.StdEncoding.DecodeString(in.Args[1])
				c.data = append(c.data, b...)
			}
		}
	case "end":
		if len(in.Args) >= 1 {
			if c := d.streams[in.Args[0]]; c != nil {
				d.clips = append(d.clips, clipRec{Ms: time.Since(d.t0).Milliseconds(), Mime: c.mime, Text: trunc(string(c.data), 400)})
				delete(d.streams, in.Args[0])
			}
		}
	case "ack":
		d.acks = append(d.acks, in.Raw)
	case "error":
		d.errs = append(d.errs, in.Raw)
	case "size":
		if len(in.Args) >= 3 && in.Args[0] == "0" {
			d.width, _ = strconv.Atoi(in.Args[1])
			d.height, _ = strconv.Atoi(in.Args[2])
		}
	}
}

func (d *driver) isClosed() bool {
	select {
	case <-d.closed:
		return true
	default:
		return false
	}
}

func (d *driver) count(op string) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.counts[op]
}

func (d *driver) clipCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.clips)
}

func (d *driver) clipsSince(n int) []clipRec {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n > len(d.clips) {
		return nil
	}
	return append([]clipRec(nil), d.clips[n:]...)
}

func (d *driver) ackCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.acks)
}

func (d *driver) acksSince(n int) []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n > len(d.acks) {
		return nil
	}
	return append([]string(nil), d.acks[n:]...)
}

func (d *driver) dims() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.width, d.height
}

func (d *driver) key(sym int) {
	d.send("key", strconv.Itoa(sym), "1")
	d.send("key", strconv.Itoa(sym), "0")
}

func keysym(r rune) int {
	if r == '\n' {
		return 0xff0d
	}
	return int(r)
}

func (d *driver) typeText(s string) {
	for _, r := range s {
		d.key(keysym(r))
		time.Sleep(12 * time.Millisecond)
	}
}

func (d *driver) ctrl(ch rune) {
	d.send("key", "65507", "1")
	d.key(int(ch))
	d.send("key", "65507", "0")
}

func (d *driver) mouse(x, y, mask int) {
	d.send("mouse", strconv.Itoa(x), strconv.Itoa(y), strconv.Itoa(mask))
	time.Sleep(40 * time.Millisecond)
}

func fileSize(p string) int64 {
	fi, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func fileCount(p, needle string) int {
	b, err := os.ReadFile(p)
	if err != nil {
		return -1
	}
	return strings.Count(string(b), needle)
}

// tailHas looks for needle in the last 64 KiB of a file.
func tailHas(p, needle string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	off := fi.Size() - 65536
	if off < 0 {
		off = 0
	}
	b := make([]byte, fi.Size()-off)
	_, _ = f.ReadAt(b, off)
	return strings.Contains(string(b), needle)
}

func tailOf(p string, n int) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return "<" + err.Error() + ">"
	}
	return printable(b, n)
}

func waitFor(timeout, poll time.Duration, cond func() bool) (time.Duration, bool) {
	start := time.Now()
	for {
		if cond() {
			return time.Since(start), true
		}
		if time.Since(start) > timeout {
			return time.Since(start), false
		}
		time.Sleep(poll)
	}
}

func ms(d time.Duration) float64 { return float64(d.Microseconds()) / 1000.0 }

func (d *driver) shellCheck(ts, tag string, a, b int) map[string]any {
	expect := fmt.Sprintf("%s-%d", tag, a*b)
	cmd := fmt.Sprintf("echo %s-$((%d*%d))\n", tag, a, b)
	start := time.Now()
	for attempt := 1; attempt <= 2; attempt++ {
		if d.isClosed() {
			break
		}
		d.typeText(cmd)
		if _, ok := waitFor(8*time.Second, 20*time.Millisecond, func() bool { return tailHas(ts, expect) || d.isClosed() }); ok && tailHas(ts, expect) {
			return map[string]any{"ok": true, "expect": expect, "attempt": attempt, "ms": time.Since(start).Milliseconds()}
		}
	}
	return map[string]any{"ok": false, "expect": expect, "typescriptTail": tailOf(ts, 300)}
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return -1
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2]
}

func maxOf(v []float64) float64 {
	m := -1.0
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

// keyDelays measures, for each keystroke, the time until the key instruction
// is in the screen file on disk and until its echo is in the typescript.
func (d *driver) keyDelays(ts, scr string, n int) map[string]any {
	const needle = "3.key,3.120,1.1" // keysym 120 ('x') pressed, as guacd writes it into the screen file
	var keyMs, tsMs []float64
	keyMiss, tsMiss := 0, 0
	for i := 0; i < n && !d.isClosed(); i++ {
		k0 := fileCount(scr, needle)
		x0 := fileCount(ts, "x")
		start := time.Now()
		d.key(120)
		kd, td := time.Duration(-1), time.Duration(-1)
		for time.Since(start) < 15*time.Second {
			if kd < 0 && fileCount(scr, needle) > k0 {
				kd = time.Since(start)
			}
			if td < 0 && fileCount(ts, "x") > x0 {
				td = time.Since(start)
			}
			if kd >= 0 && td >= 0 {
				break
			}
			time.Sleep(2 * time.Millisecond)
		}
		if kd >= 0 {
			keyMs = append(keyMs, ms(kd))
		} else {
			keyMiss++
		}
		if td >= 0 {
			tsMs = append(tsMs, ms(td))
		} else {
			tsMiss++
		}
		time.Sleep(250 * time.Millisecond)
	}
	d.ctrl('u')
	return map[string]any{
		"keystrokes": n, "screenKeyMs": keyMs, "screenKeyMaxMs": maxOf(keyMs), "screenKeyMedianMs": median(keyMs), "screenKeyMissing": keyMiss,
		"typescriptEchoMs": tsMs, "typescriptEchoMaxMs": maxOf(tsMs), "typescriptEchoMedianMs": median(tsMs), "typescriptEchoMissing": tsMiss,
		"pollIntervalMs": 2,
	}
}

func (d *driver) growth(ts, scr string, seconds int) map[string]any {
	type sample struct {
		Ms     int64 `json:"ms"`
		Screen int64 `json:"screen"`
		Ts     int64 `json:"typescript"`
		Timing int64 `json:"timing"`
	}
	var samples []sample
	start := time.Now()
	next := start
	i := 0
	for time.Since(start) < time.Duration(seconds)*time.Second && !d.isClosed() {
		if !time.Now().Before(next) {
			i++
			d.typeText(fmt.Sprintf("echo grow-%d\n", i))
			next = time.Now().Add(2 * time.Second)
		}
		samples = append(samples, sample{time.Since(start).Milliseconds(), fileSize(scr), fileSize(ts), fileSize(ts + ".timing")})
		time.Sleep(500 * time.Millisecond)
	}
	inc := func(get func(sample) int64) int {
		n := 0
		for j := 1; j < len(samples); j++ {
			if get(samples[j]) > get(samples[j-1]) {
				n++
			}
		}
		return n
	}
	return map[string]any{
		"samples":             samples,
		"screenIncreases":     inc(func(s sample) int64 { return s.Screen }),
		"typescriptIncreases": inc(func(s sample) int64 { return s.Ts }),
		"timingIncreases":     inc(func(s sample) int64 { return s.Timing }),
		"commandsTyped":       i,
		"closedDuring":        d.isClosed(),
	}
}

func (d *driver) clip(ts string) map[string]any {
	out := map[string]any{}
	d.typeText("echo COPY-$((2+3))\n")
	_, ok := waitFor(6*time.Second, 20*time.Millisecond, func() bool { return tailHas(ts, "COPY-5") })
	out["copySourceShown"] = ok
	w, h := d.dims()
	if w == 0 {
		w, h = 1024, 768
	}
	out["display"] = []int{w, h}
	n0 := d.clipCount()
	// Drag-select the whole terminal with the left button.
	d.mouse(2, 2, 0)
	d.mouse(2, 2, 1)
	d.mouse(w/2, h/2, 1)
	d.mouse(w-3, h-3, 1)
	d.mouse(w-3, h-3, 0)
	_, got := waitFor(4*time.Second, 20*time.Millisecond, func() bool { return d.clipCount() > n0 })
	clips := d.clipsSince(n0)
	out["copyReceived"] = got
	marker := false
	for _, c := range clips {
		if strings.Contains(c.Text, "COPY-5") {
			marker = true
		}
	}
	out["copyContainsMarker"] = marker
	out["copyInstructions"] = len(clips)
	// Paste: send a clipboard stream to guacd, then paste with the middle button.
	a0 := d.ackCount()
	d.send("clipboard", "7", "text/plain")
	d.send("blob", "7", base64.StdEncoding.EncodeToString([]byte("echo PASTE-$((3+4))")))
	d.send("end", "7")
	time.Sleep(800 * time.Millisecond)
	out["pasteAcks"] = d.acksSince(a0)
	d.mouse(w/2, h/2, 0)
	d.mouse(w/2, h/2, 2)
	d.mouse(w/2, h/2, 0)
	time.Sleep(300 * time.Millisecond)
	d.key(0xff0d)
	_, pasted := waitFor(4*time.Second, 20*time.Millisecond, func() bool { return tailHas(ts, "PASTE-7") })
	method := ""
	if pasted {
		method = "middle-button"
	} else {
		d.send("key", "65507", "1")
		d.send("key", "65505", "1")
		d.key('V')
		d.send("key", "65505", "0")
		d.send("key", "65507", "0")
		time.Sleep(300 * time.Millisecond)
		d.key(0xff0d)
		_, pasted = waitFor(4*time.Second, 20*time.Millisecond, func() bool { return tailHas(ts, "PASTE-7") })
		if pasted {
			method = "ctrl-shift-v"
		}
	}
	out["pasteEffective"] = pasted
	out["pasteMethod"] = method
	out["pastedTextVisible"] = tailHas(ts, "PASTE-$((3+4))")
	return out
}

func (d *driver) vnc(hold int) map[string]any {
	out := map[string]any{}
	el, ok := waitFor(15*time.Second, 50*time.Millisecond, func() bool { return d.count("img") > 0 || d.isClosed() })
	out["framebufferImg"] = ok && d.count("img") > 0
	out["firstImgMs"] = el.Milliseconds()
	w, h := d.dims()
	out["size"] = []int{w, h}
	d.mouse(200, 200, 0)
	d.typeText("echo vnc\n")
	a0 := d.ackCount()
	d.send("clipboard", "9", "text/plain")
	d.send("blob", "9", base64.StdEncoding.EncodeToString([]byte("VNCPASTE-1")))
	d.send("end", "9")
	out["pasteSentMs"] = time.Since(d.t0).Milliseconds()
	n0 := d.clipCount()
	waitFor(time.Duration(hold)*time.Second, 50*time.Millisecond, func() bool {
		for _, c := range d.clipsSince(n0) {
			if strings.Contains(c.Text, "VNCCOPY") {
				return true
			}
		}
		return d.isClosed()
	})
	got := false
	for _, c := range d.clipsSince(n0) {
		if strings.Contains(c.Text, "VNCCOPY") {
			got = true
		}
	}
	out["copyReceived"] = got
	out["clipsFromServer"] = d.clipsSince(n0)
	out["pasteAcks"] = d.acksSince(a0)
	out["imgInstructions"] = d.count("img")
	return out
}

func (d *driver) activeLoad(ts string, seconds int) map[string]any {
	cmd := fmt.Sprintf("end=$(( $(date +%%s) + %d )); while [ $(date +%%s) -lt $end ]; do ls -la /etc /usr/bin; done; echo ACTIVE-$((5+5))\n", seconds)
	start := time.Now()
	d.typeText(cmd)
	_, ok := waitFor(time.Duration(seconds+40)*time.Second, 200*time.Millisecond, func() bool { return tailHas(ts, "ACTIVE-10") || d.isClosed() })
	return map[string]any{"ok": ok && tailHas(ts, "ACTIVE-10"), "ms": time.Since(start).Milliseconds(), "typescriptBytes": fileSize(ts)}
}

type fifoReader struct {
	mu       sync.Mutex
	path     string
	opened   time.Duration
	firstAt  time.Duration
	bytes    int64
	instrs   int
	ops      map[string]int
	firstOps []string
	err      string
	done     chan struct{}
	t0       time.Time
}

type countingReader struct {
	r io.Reader
	f *fifoReader
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	if n > 0 {
		c.f.mu.Lock()
		if c.f.bytes == 0 {
			c.f.firstAt = time.Since(c.f.t0)
		}
		c.f.bytes += int64(n)
		c.f.mu.Unlock()
	}
	return n, err
}

func startFifo(path string) *fifoReader {
	f := &fifoReader{path: path, ops: map[string]int{}, done: make(chan struct{}), t0: time.Now()}
	go func() {
		defer close(f.done)
		fh, err := os.Open(path) // blocks until guacd opens the FIFO for writing
		if err != nil {
			f.mu.Lock()
			f.err = err.Error()
			f.mu.Unlock()
			return
		}
		defer fh.Close()
		f.mu.Lock()
		f.opened = time.Since(f.t0)
		f.mu.Unlock()
		p := newParser(&countingReader{fh, f})
		for {
			in, err := p.next()
			if err != nil {
				if err != io.EOF {
					f.mu.Lock()
					f.err = err.Error()
					f.mu.Unlock()
				}
				return
			}
			f.mu.Lock()
			f.instrs++
			f.ops[in.Op]++
			if len(f.firstOps) < 6 {
				f.firstOps = append(f.firstOps, trunc(in.Raw, 60))
			}
			f.mu.Unlock()
		}
	}()
	return f
}

func (f *fifoReader) report(wait time.Duration) map[string]any {
	ended := true
	select {
	case <-f.done:
	case <-time.After(wait):
		ended = false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]any{
		"path": f.path, "openedAfterMs": f.opened.Milliseconds(), "firstByteAfterMs": f.firstAt.Milliseconds(),
		"bytes": f.bytes, "instructions": f.instrs, "opcodes": f.ops, "firstInstructions": f.firstOps,
		"err": f.err, "readerSawEOF": ended,
	}
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func dial(scName, run string, sets []string) (*driver, error) {
	q := url.Values{}
	q.Set("scenario", scName)
	q.Set("run", run)
	q.Set("width", "1024")
	q.Set("height", "768")
	q.Set("dpi", "96")
	q.Set("timezone", "UTC")
	for _, s := range sets {
		q.Add("set", s)
	}
	u := "ws://127.0.0.1" + listenAddr + "/tunnel?" + q.Encode()
	dl := websocket.Dialer{Subprotocols: []string{"guacamole"}, HandshakeTimeout: 10 * time.Second, ReadBufferSize: 1 << 16}
	deadline := time.Now().Add(30 * time.Second)
	for {
		ws, resp, err := dl.Dial(u, nil)
		if err == nil {
			return &driver{ws: ws, t0: time.Now(), counts: map[string]int{}, streams: map[string]*clipBuf{}, closed: make(chan struct{})}, nil
		}
		if resp != nil && resp.StatusCode == http.StatusConflict && time.Now().Before(deadline) {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return nil, err
	}
}

func runDrive(args []string) int {
	logOut = os.Stderr
	fs := flag.NewFlagSet("drive", flag.ContinueOnError)
	scName := fs.String("scenario", "", "scenario name in the agent configuration")
	run := fs.String("run", "", "run name; names the recording files")
	script := fs.String("script", "shell", "shell|observe|growth|keys|clip|vnc|idle|active")
	tag := fs.String("tag", "MARK", "marker for the shell check")
	hold := fs.Int("hold", 0, "seconds to hold the session")
	active := fs.Int("active", 25, "seconds of terminal output for script active")
	fifo := fs.String("fifo", "", "FIFO to read the screen recording from")
	extra := fs.String("extra", "", "command typed after the script, before leaving")
	var sets multi
	fs.Var(&sets, "set", "parameter override name:value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *run == "" {
		*run = *scName
	}
	res := map[string]any{"scenario": *scName, "run": *run, "script": *script, "set": []string(sets)}
	emit := func() {
		b, _ := json.Marshal(res)
		fmt.Println("RESULT " + string(b))
	}
	var fr *fifoReader
	if *fifo != "" {
		fr = startFifo(*fifo)
	}
	d, err := dial(*scName, *run, sets)
	if err != nil {
		res["dialError"] = err.Error()
		emit()
		return 0
	}
	go d.readLoop()
	go func() {
		t := time.NewTicker(2 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-d.closed:
				return
			case <-t.C:
				d.send("nop")
			}
		}
	}()
	ts := filepath.Join(recDir, "typescript-"+*run)
	scr := filepath.Join(recDir, "screen-"+*run)
	waitFor(15*time.Second, 20*time.Millisecond, func() bool {
		d.mu.Lock()
		f := d.firstAt
		d.mu.Unlock()
		return f > 0 || d.isClosed()
	})
	time.Sleep(700 * time.Millisecond)
	switch *script {
	case "shell":
		res["shell"] = d.shellCheck(ts, *tag, 6, 7)
	case "observe":
		time.Sleep(time.Duration(*hold) * time.Second)
	case "growth":
		res["shell"] = d.shellCheck(ts, *tag, 6, 7)
		res["growth"] = d.growth(ts, scr, 12)
	case "keys":
		res["shell"] = d.shellCheck(ts, *tag, 6, 7)
		res["keys"] = d.keyDelays(ts, scr, 20)
	case "clip":
		res["shell"] = d.shellCheck(ts, *tag, 6, 7)
		res["clip"] = d.clip(ts)
	case "vnc":
		res["vnc"] = d.vnc(*hold)
	case "idle":
		res["shell"] = d.shellCheck(ts, *tag, 6, 7)
		time.Sleep(time.Duration(*hold) * time.Second)
	case "active":
		res["shell"] = d.shellCheck(ts, *tag, 6, 7)
		res["active"] = d.activeLoad(ts, *active)
	default:
		res["scriptError"] = "unknown script"
	}
	if *extra != "" && !d.isClosed() {
		d.typeText(*extra + "\n")
		time.Sleep(1500 * time.Millisecond)
	}
	res["closedBeforeEnd"] = d.isClosed()
	d.send("disconnect")
	time.Sleep(700 * time.Millisecond)
	_ = d.ws.Close()
	select {
	case <-d.closed:
	case <-time.After(3 * time.Second):
	}
	d.mu.Lock()
	res["firstInstructionMs"] = d.firstAt.Milliseconds()
	res["counts"] = d.counts
	res["errors"] = d.errs
	res["acks"] = d.acks
	res["clips"] = d.clips
	res["uuidFrame"] = d.uuid
	res["firstInstructions"] = d.firstOps
	res["closeErr"] = d.closeErr
	d.mu.Unlock()
	res["files"] = map[string]int64{"screen": fileSize(scr), "typescript": fileSize(ts), "timing": fileSize(ts + ".timing")}
	res["typescriptTail"] = tailOf(ts, 300)
	if fr != nil {
		res["fifo"] = fr.report(6 * time.Second)
	}
	emit()
	return 0
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: agent serve | agent drive [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		os.Exit(runServe())
	case "drive":
		os.Exit(runDrive(os.Args[2:]))
	default:
		fmt.Fprintln(os.Stderr, errors.New("unknown command "+os.Args[1]))
		os.Exit(2)
	}
}
