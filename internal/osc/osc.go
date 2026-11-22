// Package osc parses OSC 7501 program-status escape sequences from a raw
// terminal byte stream (such as `tmux pipe-pane -o`).
//
// Format (matching Herdr / Zellij 0.46 / spaceresume):
//
//	ESC ] 7501 ; state=<state>:app=<app>[:kind=<kind>][:msg=<base64>] (ST | BEL)
//
// where ST is ESC \ (\x1b\\) and BEL is \x07.
package osc

import (
	"encoding/base64"
	"io"
	"strings"
)

const (
	oscPrefix   = "7501;"
	maxOSCBytes = 16 * 1024
)

// Record is one parsed OSC 7501 program-status notification.
type Record struct {
	State   string
	App     string
	Profile string
	Kind    string
	Msg     string
	Raw     string
}

// ParsePayload parses the body of an OSC 7501 sequence (everything after
// "7501;" and before ST/BEL). Returns false if the payload has no recognized
// state.
func ParsePayload(body string) (Record, bool) {
	body = strings.TrimSpace(body)
	if body == "" {
		return Record{}, false
	}
	rec := Record{Raw: body}
	for _, part := range strings.Split(body, ":") {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		switch k {
		case "state":
			rec.State = normalizeState(v)
		case "app":
			rec.App = sanitizeText(v)
		case "profile":
			rec.Profile = sanitizeText(v)
		case "kind":
			rec.Kind = sanitizeText(v)
		case "msg":
			rec.Msg = decodeMsg(v)
		}
	}
	if rec.State == "" {
		return Record{}, false
	}
	return rec, true
}

func normalizeState(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "working", "busy", "running":
		return "working"
	case "idle":
		return "idle"
	case "done", "complete", "completed", "success":
		return "done"
	case "blocked", "needs_you", "needs-you", "needsyou", "waiting":
		return "blocked"
	case "error", "failed", "failure":
		return "failed"
	case "clear":
		return "clear"
	default:
		return ""
	}
}

func decodeMsg(raw string) string {
	if raw == "" {
		return ""
	}
	for _, enc := range []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	} {
		if b, err := enc.DecodeString(raw); err == nil {
			return sanitizeText(string(b))
		}
	}
	// If the sender did not base64-encode msg, sanitize and keep it readable.
	return sanitizeText(raw)
}

func sanitizeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			if r == '\t' || r == '\n' || r == '\r' {
				b.WriteByte(' ')
			}
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

type scanState int

const (
	stateGround scanState = iota
	stateESC
	stateOSC
	stateOSCESC
)

// Scanner is a streaming parser that extracts OSC 7501 records from arbitrary
// byte chunks.
type Scanner struct {
	state    scanState
	buf      []byte
	overflow bool
}

// Feed processes chunk and invokes onRecord for each valid OSC 7501 record
// completed within chunk.
func (s *Scanner) Feed(chunk []byte, onRecord func(Record)) {
	for _, b := range chunk {
		switch s.state {
		case stateGround:
			if b == 0x1b {
				s.state = stateESC
			}
		case stateESC:
			switch b {
			case ']':
				s.state = stateOSC
				s.buf = s.buf[:0]
				s.overflow = false
			case 0x1b:
				s.state = stateESC
			default:
				s.state = stateGround
			}
		case stateOSC:
			switch b {
			case 0x07: // BEL terminates OSC
				s.finish(onRecord)
				s.state = stateGround
			case 0x1b: // possible ST (ESC \)
				s.state = stateOSCESC
			case 0x18, 0x1a: // CAN / SUB aborts escape sequence
				s.reset()
			default:
				if !s.overflow {
					if len(s.buf) < maxOSCBytes {
						s.buf = append(s.buf, b)
					} else {
						s.overflow = true
						s.buf = s.buf[:0]
					}
				}
			}
		case stateOSCESC:
			switch b {
			case '\\': // ST terminates OSC
				s.finish(onRecord)
				s.state = stateGround
			case ']': // New OSC started immediately
				s.buf = s.buf[:0]
				s.overflow = false
				s.state = stateOSC
			case 0x1b:
				// Stay in stateOSCESC so tmux DCS passthrough doubled escapes
				// (\x1bPtmux;\x1b\x1b]7501;...\x1b\x1b\\\x1b\\) terminate cleanly.
				s.state = stateOSCESC
			default:
				s.reset()
			}
		}
	}
}

func (s *Scanner) finish(onRecord func(Record)) {
	if s.overflow {
		s.reset()
		return
	}
	raw := string(s.buf)
	s.buf = s.buf[:0]
	if !strings.HasPrefix(raw, oscPrefix) {
		return
	}
	if rec, ok := ParsePayload(raw[len(oscPrefix):]); ok && onRecord != nil {
		onRecord(rec)
	}
}

func (s *Scanner) reset() {
	s.state = stateGround
	s.buf = s.buf[:0]
	s.overflow = false
}

// Watch reads raw terminal bytes from r until EOF and invokes onRecord for
// every OSC 7501 record encountered.
func Watch(r io.Reader, onRecord func(Record)) error {
	var sc Scanner
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sc.Feed(buf[:n], onRecord)
		}
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
	}
}
