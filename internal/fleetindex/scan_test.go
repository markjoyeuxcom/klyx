package fleetindex

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"time"
	"unicode/utf8"
)

// jsonString writes v as a JSON string, choosing among equivalent escapes
// at random (and passing invalid UTF-8 bytes through raw).
func jsonString(rnd *rand.Rand, b *bytes.Buffer, v string) {
	b.WriteByte('"')
	for i := 0; i < len(v); {
		r, size := rune(v[i]), 1
		if v[i] >= 0x80 {
			r, size = decodeRune(v[i:])
		}
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r < 0x20:
			fmt.Fprintf(b, `\u%04x`, r)
		case r == '/' && rnd.Intn(2) == 0:
			b.WriteString(`\/`)
		case r > 0xFFFF && rnd.Intn(2) == 0:
			r1, r2 := surrogates(r)
			fmt.Fprintf(b, `\u%04x\u%04X`, r1, r2)
		case r < 0x80 && rnd.Intn(8) == 0:
			fmt.Fprintf(b, `\u%04x`, r)
		default:
			b.WriteString(v[i : i+size])
		}
		i += size
	}
	b.WriteByte('"')
}

func decodeRune(s string) (rune, int) {
	r, size := utf8.DecodeRuneInString(s)
	return r, size // an invalid byte decodes as (RuneError, 1) and is written raw
}

func surrogates(r rune) (rune, rune) {
	r -= 0x10000
	return 0xD800 + (r>>10)&0x3FF, 0xDC00 + r&0x3FF
}

var alphabet = []string{"a", "z", "0", "-", ".", "/", "\"", "\\", "\n", "\t", "é", "日", "😀", "\x01", "\xff", " ", " "}

func randString(rnd *rand.Rand, n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		b.WriteString(alphabet[rnd.Intn(len(alphabet))])
	}
	return b.String()
}

// randValue writes an arbitrary JSON value to be skipped.
func randValue(rnd *rand.Rand, b *bytes.Buffer, depth int) {
	switch k := rnd.Intn(7); {
	case k == 0 && depth < 4:
		b.WriteByte('{')
		for i, n := 0, rnd.Intn(4); i < n; i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			jsonString(rnd, b, randString(rnd, 5))
			b.WriteString(" : ")
			randValue(rnd, b, depth+1)
		}
		b.WriteByte('}')
	case k == 1 && depth < 4:
		b.WriteString("[ ")
		for i, n := 0, rnd.Intn(4); i < n; i++ {
			if i > 0 {
				b.WriteString(" ,")
			}
			randValue(rnd, b, depth+1)
		}
		b.WriteByte(']')
	case k == 2:
		b.WriteString([]string{"true", "false", "null", "-12.5e3", "0", "42"}[rnd.Intn(6)])
	default:
		jsonString(rnd, b, randString(rnd, rnd.Intn(40)))
	}
}

type genDoc struct {
	name, ns, uid, rv, created string
	labels                     map[string]string
}

// genObject writes one object with metadata among skipped fields, its
// fields in random order.
func genObject(rnd *rand.Rand) ([]byte, genDoc) {
	d := genDoc{name: randString(rnd, rnd.Intn(30)), ns: randString(rnd, rnd.Intn(10)), uid: randString(rnd, 12), rv: fmt.Sprint(rnd.Intn(1e6)), created: "2026-10-15T10:00:00Z"}
	if rnd.Intn(3) == 0 {
		d.created = randString(rnd, 5)
	}
	if rnd.Intn(4) > 0 {
		d.labels = map[string]string{}
		for i, n := 0, rnd.Intn(6); i < n; i++ {
			d.labels[fmt.Sprintf("k%d-%s", i, randString(rnd, 4))] = randString(rnd, rnd.Intn(12))
		}
	}
	var b bytes.Buffer
	field := func(k string, v func()) { jsonString(rnd, &b, k); b.WriteString(":"); v() }
	str := func(v string) func() { return func() { jsonString(rnd, &b, v) } }
	meta := []func(){
		func() { field("name", str(d.name)) },
		func() { field("namespace", str(d.ns)) },
		func() { field("uid", str(d.uid)) },
		func() { field("resourceVersion", str(d.rv)) },
		func() { field("creationTimestamp", str(d.created)) },
		func() { field("annotations", func() { randValue(rnd, &b, 0) }) },
		func() { field("managedFields", func() { randValue(rnd, &b, 0) }) },
	}
	if d.labels != nil {
		meta = append(meta, func() {
			field("labels", func() {
				b.WriteByte('{')
				i := 0
				for k, v := range d.labels {
					if i > 0 {
						b.WriteByte(',')
					}
					jsonString(rnd, &b, k)
					b.WriteByte(':')
					jsonString(rnd, &b, v)
					i++
				}
				b.WriteByte('}')
			})
		})
	}
	rnd.Shuffle(len(meta), func(i, j int) { meta[i], meta[j] = meta[j], meta[i] })
	top := []func(){
		func() { field("kind", str("PartialObjectMetadata")) },
		func() { field("spec", func() { randValue(rnd, &b, 0) }) },
		func() {
			field("metadata", func() {
				b.WriteString("{ ")
				for i, f := range meta {
					if i > 0 {
						b.WriteString(", ")
					}
					f()
				}
				b.WriteString(" }")
			})
		},
	}
	rnd.Shuffle(len(top), func(i, j int) { top[i], top[j] = top[j], top[i] })
	b.WriteString("{\n")
	for i, f := range top {
		if i > 0 {
			b.WriteByte(',')
		}
		f()
	}
	b.WriteString("}\n")
	return b.Bytes(), d
}

type stdObject struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name              string            `json:"name"`
		Namespace         string            `json:"namespace"`
		UID               string            `json:"uid"`
		ResourceVersion   string            `json:"resourceVersion"`
		CreationTimestamp string            `json:"creationTimestamp"`
		Labels            map[string]string `json:"labels"`
	} `json:"metadata"`
}

// §5: the scanner reads what encoding/json reads, whatever the escapes,
// field order, skipped values and read boundaries.
func TestScannerMatchesEncodingJSON(t *testing.T) {
	rnd := rand.New(rand.NewSource(9))
	readers := map[string]func(io.Reader) io.Reader{
		"whole": func(r io.Reader) io.Reader { return r },
		"byte":  iotest.OneByteReader,
		"half":  iotest.HalfReader,
	}
	for i := 0; i < 3000; i++ {
		doc, _ := genObject(rnd)
		var want stdObject
		if err := json.Unmarshal(doc, &want); err != nil {
			t.Fatalf("generator wrote invalid JSON: %v\n%s", err, doc)
		}
		for name, wrap := range readers {
			var got scanned
			if err := newScanner(wrap(bytes.NewReader(doc))).object(&got); err != nil {
				t.Fatalf("%s: %v\n%s", name, err, doc)
			}
			m := want.Metadata
			if got.kind != want.Kind || got.src.Name != m.Name || got.src.Namespace != m.Namespace || got.src.UID != m.UID || got.rv != m.ResourceVersion {
				t.Fatalf("%s: identity %+v vs %+v\n%s", name, got, want, doc)
			}
			ct, err := time.Parse(time.RFC3339, m.CreationTimestamp)
			if got.src.NoCreated != (err != nil) || (err == nil && !got.src.Created.Equal(ct)) {
				t.Fatalf("%s: created %v %v vs %q", name, got.src.Created, got.src.NoCreated, m.CreationTimestamp)
			}
			labels := map[string]string{}
			for _, l := range got.src.kept {
				labels[l.Key] = l.Value
			}
			if len(m.Labels) == 0 && len(labels) == 0 {
				continue
			}
			if !reflect.DeepEqual(labels, m.Labels) || got.src.truncated || got.src.hash != labelHash(m.Labels) {
				t.Fatalf("%s: labels %v vs %v\n%s", name, labels, m.Labels, doc)
			}
		}
	}
}

func TestScannerBounds(t *testing.T) {
	read := func(doc string) (scanned, error) {
		var out scanned
		return out, newScanner(strings.NewReader(doc)).object(&out)
	}
	big := strings.Repeat("x", fieldBytes+1)
	for _, doc := range []string{
		`{"metadata":{"name":"` + big + `"}}`,
		`{"metadata":{"uid":"` + big + `"}}`,
		`{"metadata":{"labels":{"a":"` + big + `"}}}`,
		`{"metadata":{"labels":{"` + big + `":"v"}}}`,
	} {
		if _, err := read(doc); !errors.Is(err, ErrOversize) {
			t.Fatalf("want oversize, got %v", err)
		}
	}
	// a field exactly at its bound is kept
	if out, err := read(`{"metadata":{"name":"` + big[1:] + `"}}`); err != nil || len(out.src.Name) != fieldBytes {
		t.Fatalf("at the bound: %v", err)
	}
	// a status message is truncated, never an error
	if out, err := read(`{"kind":"Status","code":410,"reason":"Expired","message":"` + strings.Repeat("m", 5000) + `"}`); err != nil || out.code != 410 || len(out.message) != statusBytes {
		t.Fatalf("status: %+v %v", out, err)
	}
	// labels past 2 KiB: the first in the order sent are kept, all hashed
	var b strings.Builder
	b.WriteString(`{"metadata":{"labels":{`)
	want := map[string]string{}
	for i := 0; i < 100; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		k, v := fmt.Sprintf("k%03d", i), strings.Repeat("v", 40)
		want[k] = v
		fmt.Fprintf(&b, "%q:%q", k, v)
	}
	b.WriteString(`}}}`)
	out, err := read(b.String())
	if err != nil || !out.src.truncated || len(out.src.kept) != LabelBytes/44 || out.src.kept[0].Key != "k000" || out.src.hash != labelHash(want) {
		t.Fatalf("labels: %d kept, truncated %v, %v", len(out.src.kept), out.src.truncated, err)
	}
	// huge skipped values pass through the fixed buffer
	huge := `{"metadata":{"name":"n","annotations":{"a":"` + strings.Repeat("y", 3<<20) + `"},"managedFields":[` + strings.Repeat(`{"f:x":{"f:y":[1,2,{"z":"\"]}"}]}},`, 50000) + `{}]}}`
	if out, err := read(huge); err != nil || out.src.Name != "n" {
		t.Fatalf("huge: %v", err)
	}
	// malformed and truncated input fails, never panics
	for _, doc := range []string{`{"metadata":{"name":"n"`, `{"metadata":{"name":"\q"}}`, `{"metadata":{"name":"a` + "\x01" + `"}}`, `{"metadata" "x"}`, `[`, ``, `{"metadata":{"name":"\ud83d`} {
		if _, err := read(doc); err == nil {
			t.Fatalf("accepted %q", doc)
		}
	}
}

// Native fuzzing: whatever the input, the scanner returns (never panics,
// never loops) and what it keeps stays within its bounds.
func FuzzScanner(f *testing.F) {
	rnd := rand.New(rand.NewSource(1))
	for i := 0; i < 20; i++ {
		doc, _ := genObject(rnd)
		f.Add(doc)
	}
	f.Add([]byte(`{"metadata":{"name":"\ud83d\ude00\udc00\ud800x"}}`))
	f.Fuzz(func(t *testing.T, doc []byte) {
		var out scanned
		if newScanner(bytes.NewReader(doc)).object(&out) != nil {
			return
		}
		// invalid UTF-8 reads as U+FFFD (3 bytes per byte): the bound on
		// the bytes read, times three
		for _, v := range []string{out.src.Name, out.src.Namespace, out.src.UID, out.rv} {
			if len(v) > 3*fieldBytes {
				t.Fatalf("a kept field of %d bytes", len(v))
			}
		}
		n := 0
		for _, l := range out.src.kept {
			n += len(l.Key) + len(l.Value)
		}
		if n > LabelBytes {
			t.Fatalf("labels kept %d bytes", n)
		}
	})
}
