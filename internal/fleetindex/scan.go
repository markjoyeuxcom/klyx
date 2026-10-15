package fleetindex

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
	"unicode/utf16"
	"unicode/utf8"
)

// The bounded scanner (slice 9 note §2.5). A JSON decoder buffers a whole
// value before decoding it and keeps the enlarged buffer afterwards, so one
// large object on a long watch would stay retained beyond what the budget
// charged. This scanner reads through a fixed buffer, keeps only an entry's
// fields - each bounded - and skips every other value as it streams past,
// never buffering it, however large.

const (
	scanBuffer    = 32 << 10 // the read buffer, never grown
	fieldBytes    = 1 << 10  // a kept identity field: name, namespace, UID, resourceVersion, creationTimestamp
	keyBytes      = 64       // an object key we dispatch on; a longer one is skipped
	continueBytes = 4 << 10  // a list's continue token
	statusBytes   = 1 << 10  // an ERROR status's reason or message, truncated past it
)

// ErrOversize: a kept field is over its bound. No valid Kubernetes object
// has one; the read stops and the kind reads "oversize".
var ErrOversize = errors.New("fleetindex: an object's metadata is over the scanner's bounds")

type scanner struct {
	r        io.Reader
	buf      [scanBuffer]byte
	pos, end int
	scratch  [continueBytes + 4]byte // a kept string, decoded: the largest bound a read declares
}

func newScanner(r io.Reader) *scanner { return &scanner{r: r} }

func (s *scanner) fill() error {
	if s.pos < s.end {
		return nil
	}
	for {
		n, err := s.r.Read(s.buf[:])
		s.pos, s.end = 0, n
		if n > 0 {
			return nil
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}
			return err
		}
	}
}

func (s *scanner) byte() (byte, error) {
	if err := s.fill(); err != nil {
		return 0, err
	}
	c := s.buf[s.pos]
	s.pos++
	return c, nil
}

// peek returns the next non-space byte without consuming it.
func (s *scanner) peek() (byte, error) {
	for {
		if err := s.fill(); err != nil {
			return 0, err
		}
		c := s.buf[s.pos]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			s.pos++
			continue
		}
		return c, nil
	}
}

// atEOF reports whether only whitespace remains.
func (s *scanner) atEOF() (bool, error) {
	_, err := s.peek()
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return true, nil
	}
	return false, err
}

func (s *scanner) expect(want byte) error {
	c, err := s.peek()
	if err != nil {
		return err
	}
	if c != want {
		return fmt.Errorf("fleetindex: want %q, got %q", want, c)
	}
	s.pos++
	return nil
}

// str reads a string of at most limit decoded bytes. Past the limit it
// consumes the rest and reports over, returning the prefix kept. Escapes,
// surrogate pairs and invalid UTF-8 decode as encoding/json decodes them.
func (s *scanner) str(limit int) (string, bool, error) {
	if err := s.expect('"'); err != nil {
		return "", false, err
	}
	n, over := 0, false
	if limit > continueBytes {
		limit = continueBytes
	}
	put := func(b ...byte) {
		if over || n+len(b) > limit {
			over = true
			return
		}
		n += copy(s.scratch[n:], b)
	}
	putRune := func(r rune) {
		var b [4]byte
		put(b[:utf8.EncodeRune(b[:], r)]...)
	}
	var hi rune // a high surrogate waiting for its pair
	for {
		c, err := s.byte()
		if err != nil {
			return "", false, err
		}
		if hi != 0 && c != '\\' {
			putRune(utf8.RuneError)
			hi = 0
		}
		switch {
		case c == '"':
			return s.text(n), over, nil
		case c == '\\':
			e, err := s.byte()
			if err != nil {
				return "", false, err
			}
			if hi != 0 && e != 'u' {
				putRune(utf8.RuneError)
				hi = 0
			}
			switch e {
			case '"', '\\', '/':
				put(e)
			case 'b':
				put('\b')
			case 'f':
				put('\f')
			case 'n':
				put('\n')
			case 'r':
				put('\r')
			case 't':
				put('\t')
			case 'u':
				r, err := s.hex4()
				if err != nil {
					return "", false, err
				}
				if hi != 0 {
					if p := utf16.DecodeRune(hi, r); p != utf8.RuneError {
						putRune(p)
						hi = 0
						continue
					}
					putRune(utf8.RuneError)
					hi = 0
				}
				switch {
				case r >= 0xD800 && r < 0xDC00:
					hi = r
				case r >= 0xDC00 && r < 0xE000:
					putRune(utf8.RuneError)
				default:
					putRune(r)
				}
			default:
				return "", false, fmt.Errorf("fleetindex: bad escape %q", e)
			}
		case c < 0x20:
			return "", false, fmt.Errorf("fleetindex: control character in string")
		default:
			put(c)
		}
	}
}

// text is the kept prefix as a string; invalid UTF-8 bytes read as U+FFFD,
// one per byte, as encoding/json reads them.
func (s *scanner) text(n int) string {
	b := s.scratch[:n]
	if utf8.Valid(b) {
		return string(b)
	}
	out := make([]rune, 0, n)
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		out = append(out, r)
		b = b[size:]
	}
	return string(out)
}

func (s *scanner) hex4() (rune, error) {
	var r rune
	for i := 0; i < 4; i++ {
		c, err := s.byte()
		if err != nil {
			return 0, err
		}
		switch {
		case c >= '0' && c <= '9':
			r = r<<4 | rune(c-'0')
		case c >= 'a' && c <= 'f':
			r = r<<4 | rune(c-'a'+10)
		case c >= 'A' && c <= 'F':
			r = r<<4 | rune(c-'A'+10)
		default:
			return 0, fmt.Errorf("fleetindex: bad \\u escape")
		}
	}
	return r, nil
}

// skipString consumes a string without keeping it, validating it as
// encoding/json would: known escapes only, no control characters.
func (s *scanner) skipString() error {
	if err := s.expect('"'); err != nil {
		return err
	}
	for {
		c, err := s.byte()
		if err != nil {
			return err
		}
		switch {
		case c == '"':
			return nil
		case c == '\\':
			e, err := s.byte()
			if err != nil {
				return err
			}
			switch e {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
			case 'u':
				if _, err := s.hex4(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("fleetindex: bad escape %q", e)
			}
		case c < 0x20:
			return fmt.Errorf("fleetindex: control character in string")
		}
	}
}

// maxDepth bounds a skipped value's nesting (the scanner's recursion).
const maxDepth = 512

// skip consumes one value of any size without keeping it, validating its
// grammar as it streams past.
func (s *scanner) skip() error { return s.skipAt(0) }

func (s *scanner) skipAt(depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("fleetindex: a value nested deeper than %d", maxDepth)
	}
	c, err := s.peek()
	if err != nil {
		return err
	}
	switch c {
	case '"':
		return s.skipString()
	case '{':
		s.pos++
		if c, err := s.peek(); err != nil {
			return err
		} else if c == '}' {
			s.pos++
			return nil
		}
		for {
			if err := s.skipString(); err != nil {
				return err
			}
			if err := s.expect(':'); err != nil {
				return err
			}
			if err := s.skipAt(depth + 1); err != nil {
				return err
			}
			c, err := s.peek()
			if err != nil {
				return err
			}
			s.pos++
			switch c {
			case ',':
			case '}':
				return nil
			default:
				return fmt.Errorf("fleetindex: want ',' or '}', got %q", c)
			}
		}
	case '[':
		s.pos++
		if c, err := s.peek(); err != nil {
			return err
		} else if c == ']' {
			s.pos++
			return nil
		}
		for {
			if err := s.skipAt(depth + 1); err != nil {
				return err
			}
			c, err := s.peek()
			if err != nil {
				return err
			}
			s.pos++
			switch c {
			case ',':
			case ']':
				return nil
			default:
				return fmt.Errorf("fleetindex: want ',' or ']', got %q", c)
			}
		}
	default:
		_, err := s.literal()
		return err
	}
}

// literal reads a number, true, false or null, validated (bounded: a
// literal has no legitimate reason to be long).
func (s *scanner) literal() (string, error) {
	if _, err := s.peek(); err != nil {
		return "", err
	}
	var b [64]byte
	n := 0
	for {
		if err := s.fill(); err != nil {
			if n > 0 && errors.Is(err, io.ErrUnexpectedEOF) {
				break
			}
			return "", err
		}
		c := s.buf[s.pos]
		if c == ',' || c == '}' || c == ']' || c == ':' || c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			break
		}
		if n >= len(b) {
			return "", fmt.Errorf("fleetindex: literal too long")
		}
		b[n] = c
		n++
		s.pos++
	}
	lit := b[:n]
	switch string(lit) {
	case "true", "false", "null":
		return string(lit), nil
	}
	if !validNumber(lit) {
		return "", fmt.Errorf("fleetindex: not a JSON value: %q", lit)
	}
	return string(lit), nil
}

// validNumber is JSON's number grammar.
func validNumber(b []byte) bool {
	i := 0
	if i < len(b) && b[i] == '-' {
		i++
	}
	switch {
	case i < len(b) && b[i] == '0':
		i++
	case i < len(b) && b[i] >= '1' && b[i] <= '9':
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	default:
		return false
	}
	if i < len(b) && b[i] == '.' {
		i++
		if i >= len(b) || b[i] < '0' || b[i] > '9' {
			return false
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	if i < len(b) && (b[i] == 'e' || b[i] == 'E') {
		i++
		if i < len(b) && (b[i] == '+' || b[i] == '-') {
			i++
		}
		if i >= len(b) || b[i] < '0' || b[i] > '9' {
			return false
		}
		for i < len(b) && b[i] >= '0' && b[i] <= '9' {
			i++
		}
	}
	return i == len(b)
}

// fields calls f with each key of an object; f must consume its value. A
// key longer than keyBytes is none we dispatch on: its value is skipped. A
// null object has no fields.
func (s *scanner) fields(f func(key string) error) error {
	return s.fieldsWith(keyBytes, func(key string, over bool) error {
		if over {
			return s.skip()
		}
		return f(key)
	})
}

// fieldsWith reads each key within limit and calls f, which must consume
// the value.
func (s *scanner) fieldsWith(limit int, f func(key string, over bool) error) error {
	c, err := s.peek()
	if err != nil {
		return err
	}
	if c == 'n' {
		_, err := s.literal()
		return err
	}
	if err := s.expect('{'); err != nil {
		return err
	}
	if c, err := s.peek(); err != nil {
		return err
	} else if c == '}' {
		s.pos++
		return nil
	}
	for {
		key, over, err := s.str(limit)
		if err != nil {
			return err
		}
		if err := s.expect(':'); err != nil {
			return err
		}
		if err := f(key, over); err != nil {
			return err
		}
		c, err := s.peek()
		if err != nil {
			return err
		}
		s.pos++
		switch c {
		case ',':
		case '}':
			return nil
		default:
			return fmt.Errorf("fleetindex: want ',' or '}', got %q", c)
		}
	}
}

// elements calls f for each element of an array; f must consume it. A null
// array has none.
func (s *scanner) elements(f func() error) error {
	c, err := s.peek()
	if err != nil {
		return err
	}
	if c == 'n' {
		_, err := s.literal()
		return err
	}
	if err := s.expect('['); err != nil {
		return err
	}
	if c, err := s.peek(); err != nil {
		return err
	} else if c == ']' {
		s.pos++
		return nil
	}
	for {
		if err := f(); err != nil {
			return err
		}
		c, err := s.peek()
		if err != nil {
			return err
		}
		s.pos++
		switch c {
		case ',':
		case ']':
			return nil
		default:
			return fmt.Errorf("fleetindex: want ',' or ']', got %q", c)
		}
	}
}

// kept reads a bounded identity field; null reads as "".
func (s *scanner) kept() (string, error) {
	c, err := s.peek()
	if err != nil {
		return "", err
	}
	if c != '"' {
		return "", s.skip()
	}
	v, over, err := s.str(fieldBytes)
	if err != nil {
		return "", err
	}
	if over {
		return "", ErrOversize
	}
	return v, nil
}

// meta scans an object's metadata into src (and its resourceVersion).
func (s *scanner) meta(src *Source, rv *string) error {
	created := ""
	err := s.fields(func(key string) (err error) {
		switch key {
		case "name":
			src.Name, err = s.kept()
		case "namespace":
			src.Namespace, err = s.kept()
		case "uid":
			src.UID, err = s.kept()
		case "resourceVersion":
			*rv, err = s.kept()
		case "creationTimestamp":
			created, err = s.kept()
		case "labels":
			err = s.labels(src)
		default:
			err = s.skip()
		}
		return err
	})
	if err != nil {
		return err
	}
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		src.Created = t
	} else {
		src.NoCreated = true
	}
	return nil
}

// labels keeps the first MaxLabels labels, at most LabelBytes, in the order
// sent (key order from a Kubernetes API server), and hashes every one. An
// empty key (never a valid label) is hashed but not kept. A key or value
// over fieldBytes is oversize; a null value reads as "", as encoding/json
// reads it into a map.
func (s *scanner) labels(src *Source) error {
	src.scanned, src.kept, src.truncated, src.hash = true, nil, false, 0
	total, full := 0, false
	return s.fieldsWith(fieldBytes, func(k string, over bool) error {
		if over {
			return ErrOversize
		}
		c, err := s.peek()
		if err != nil {
			return err
		}
		v := ""
		if c == '"' {
			var vover bool
			if v, vover, err = s.str(fieldBytes); err != nil {
				return err
			} else if vover {
				return ErrOversize
			}
		} else if err := s.skip(); err != nil {
			return err
		}
		src.hash += pairHash(k, v)
		switch {
		case k == "":
			src.truncated = true
		case !full && len(src.kept) < MaxLabels && total+len(k)+len(v) <= LabelBytes:
			src.kept = append(src.kept, Label{Key: k, Value: v})
			total += len(k) + len(v)
		default:
			full, src.truncated = true, true
		}
		return nil
	})
}

// object scans one list item or watch object: its metadata, and an ERROR
// status's code, reason and message.
type scanned struct {
	src     Source
	rv      string
	kind    string
	code    int
	reason  string
	message string
}

func (s *scanner) object(out *scanned) error {
	return s.fields(func(key string) error {
		switch key {
		case "kind":
			v, _, err := s.str(keyBytes)
			out.kind = v
			return err
		case "metadata":
			return s.meta(&out.src, &out.rv)
		case "code":
			lit, err := s.literal()
			if err != nil {
				return err
			}
			out.code, _ = strconv.Atoi(lit)
			return nil
		case "reason":
			return s.statusString(&out.reason)
		case "message":
			return s.statusString(&out.message)
		default:
			return s.skip()
		}
	})
}

func (s *scanner) statusString(dst *string) error {
	c, err := s.peek()
	if err != nil {
		return err
	}
	if c != '"' {
		return s.skip()
	}
	v, _, err := s.str(statusBytes) // truncated past its bound
	*dst = v
	return err
}
