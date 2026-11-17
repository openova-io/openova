package meter

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// A stream is "data: <chunk>" events ended by blank lines, then "data: [DONE]"; the usage
// chunk before it has empty "choices" (docs/contracts/model-contract.md, section 1.4).

// MaxRequestBytes is the largest body of POST /v1/chat/completions; a larger one gets 413.
const MaxRequestBytes = 16 << 20

// maxStreamLine is the longest line the parser holds to read; a longer one is forwarded unread.
const maxStreamLine = MaxBodyBytes

// StreamParser consumes server-sent-event bytes as they pass and reports usage at the end.
type StreamParser interface {
	// Write returns the bytes to forward to the caller. It may hold back the final usage-only chunk.
	Write(p []byte) (forward []byte)
	// Close returns the bytes still held and the usage seen.
	Close() (forward []byte, u Usage)
}

// NewStreamParser returns a parser. dropUsageChunk is true when the caller did not ask for usage.
func NewStreamParser(dropUsageChunk bool) StreamParser { return &sseParser{drop: dropUsageChunk} }

// sseParser forwards each line when the line has ended, reads the "data:" lines, keeps the last
// usage object, counts the starts of tool calls and notes "data: [DONE]".
type sseParser struct {
	drop     bool   // hold back the usage-only chunk
	line     []byte // the line that has not ended yet
	long     bool   // that line is too long to read and is forwarded as it arrives
	skipping bool   // a usage-only chunk was held back: hold back the rest of its event
	done     bool   // "data: [DONE]" was seen
	usage    Usage
}

// Write implements StreamParser.
func (s *sseParser) Write(p []byte) []byte {
	var out []byte
	for len(p) > 0 {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			if s.long {
				return append(out, p...)
			}
			if s.line = append(s.line, p...); len(s.line) > maxStreamLine {
				out, s.line, s.long = append(out, s.line...), nil, true
			}
			return out
		}
		piece := p[:i+1]
		p = p[i+1:]
		if s.long {
			out, s.long = append(out, piece...), false
			continue
		}
		s.line = append(s.line, piece...)
		out = append(out, s.take()...)
	}
	return out
}

// Close implements StreamParser. A last line without its end is read like any other line.
func (s *sseParser) Close() ([]byte, Usage) { return s.take(), s.usage }

// take reads the held line and returns what to forward for it: the line, or nothing when the
// line belongs to a usage-only chunk that is held back.
func (s *sseParser) take() []byte {
	line := s.line
	s.line = nil
	text := bytes.TrimRight(line, "\r\n")
	switch {
	case len(text) == 0: // a blank line ends an event, the held-back one too
		if s.skipping {
			s.skipping = false
			return nil
		}
		return line
	case s.skipping:
		return nil
	}
	data, ok := bytes.CutPrefix(text, []byte("data:"))
	if !ok {
		return line
	}
	if data = bytes.TrimSpace(data); string(data) == "[DONE]" {
		s.done = true
	} else if s.read(data) && s.drop {
		s.skipping = true
		return nil
	}
	return line
}

// read parses one chunk. It keeps the chunk's usage object, and counts a tool-call delta that
// carries an "id", the first delta of a call. It reports whether the chunk is usage-only.
func (s *sseParser) read(data []byte) bool {
	var c struct {
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				ToolCalls []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *usageBlock `json:"usage"`
	}
	if json.Unmarshal(data, &c) != nil {
		return false
	}
	if c.Model != "" {
		s.usage.ResponseModel = c.Model
	}
	for _, ch := range c.Choices {
		for _, tc := range ch.Delta.ToolCalls {
			if tc.ID != "" {
				s.usage.ToolCalls++
			}
		}
	}
	if c.Usage == nil {
		return false
	}
	s.usage.PromptTokens = c.Usage.PromptTokens
	s.usage.CachedTokens = c.Usage.PromptTokensDetails.CachedTokens
	s.usage.CacheWriteTokens = c.Usage.PromptTokensDetails.CacheWriteTokens
	s.usage.CompletionTokens = c.Usage.CompletionTokens
	s.usage.ReasoningTokens = c.Usage.CompletionTokensDetails.ReasoningTokens
	s.usage.Complete = true
	return len(c.Choices) == 0
}

// meteredBody is a streamed body as the proxy copies it to the caller: the back end's bytes
// passed through the parser, and at the end the bytes the parser still holds.
type meteredBody struct {
	rc       io.ReadCloser
	p        *sseParser
	buf, out []byte
	err      error
}

// Read implements io.Reader.
func (m *meteredBody) Read(b []byte) (int, error) {
	for len(m.out) == 0 {
		if m.err != nil {
			return 0, m.err
		}
		n, err := m.rc.Read(m.buf)
		m.out = append(m.out, m.p.Write(m.buf[:n])...)
		if errors.Is(err, io.EOF) {
			rest, _ := m.p.Close()
			m.out = append(m.out, rest...)
		}
		m.err = err
	}
	n := copy(b, m.out)
	m.out = m.out[n:]
	return n, nil
}

// Close implements io.Closer.
func (m *meteredBody) Close() error { return m.rc.Close() }

// withStreamUsage returns the body to forward for POST /v1/chat/completions, and whether the
// usage chunk of the stream is held back from the caller. A request with "stream": true is
// forwarded with "stream_options.include_usage": true; the chunk is held back when the caller
// had not set it. Any other body is forwarded unchanged.
func withStreamUsage(body []byte) ([]byte, bool) {
	var doc map[string]json.RawMessage
	var stream, asked bool
	if json.Unmarshal(body, &doc) != nil || json.Unmarshal(doc["stream"], &stream) != nil || !stream {
		return body, true
	}
	opts := map[string]json.RawMessage{}
	if raw := doc["stream_options"]; raw != nil && json.Unmarshal(raw, &opts) != nil {
		return body, true
	}
	if opts == nil {
		opts = map[string]json.RawMessage{}
	}
	if json.Unmarshal(opts["include_usage"], &asked) == nil && asked {
		return body, false
	}
	opts["include_usage"] = json.RawMessage("true")
	raw, err := marshalNoEscape(opts)
	if err != nil {
		return body, true
	}
	doc["stream_options"] = raw
	if out, err := marshalNoEscape(doc); err == nil {
		return out, true
	}
	return body, true
}

// marshalNoEscape encodes v without escaping <, > and &, so the caller's text reaches the back
// end as it was written.
func marshalNoEscape(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	err := enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n"), err
}
