package fleetindex

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// The index's own reads (slice 9 note §2.5): plain REST requests whose
// Accept names only the metadata forms - never the metadata client, whose
// Accept ends with a bare application/json under which a server without
// metadata support would send whole objects - read by the bounded scanner
// (scan.go) one item or event at a time, so no page, queue or enlarged
// buffer is ever held.

const (
	acceptList  = "application/json;as=PartialObjectMetadataList;g=meta.k8s.io;v=v1"
	acceptWatch = "application/json;as=PartialObjectMetadata;g=meta.k8s.io;v=v1"
	// PageSize is the list page (parent §8.4).
	PageSize = 500
)

// StatusError is an HTTP or watch failure with its status code.
type StatusError struct {
	Code    int
	Reason  string
	Message string
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("%d %s: %s", e.Code, e.Reason, e.Message)
	}
	return fmt.Sprintf("%d %s", e.Code, e.Reason)
}

// codeOf is err's status code, or 0.
func codeOf(err error) int {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code
	}
	return 0
}

// Reader reads one cluster's metadata through an authenticated client.
type Reader struct {
	Client *http.Client
	Host   string // the API server's base URL
}

func resourcePath(gvr schema.GroupVersionResource) string {
	if gvr.Group == "" {
		return "/api/" + gvr.Version + "/" + gvr.Resource
	}
	return "/apis/" + gvr.Group + "/" + gvr.Version + "/" + gvr.Resource
}

func (r *Reader) get(ctx context.Context, gvr schema.GroupVersionResource, q url.Values, accept string) (io.ReadCloser, error) {
	u := strings.TrimSuffix(r.Host, "/") + resourcePath(gvr) + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		defer func() { _ = resp.Body.Close() }()
		se := &StatusError{Code: resp.StatusCode, Reason: http.StatusText(resp.StatusCode)}
		var st scanned
		if newScanner(io.LimitReader(resp.Body, 64<<10)).object(&st) == nil {
			if st.reason != "" {
				se.Reason = st.reason
			}
			se.Message = st.message
		}
		return nil, se
	}
	// The API server names the metadata form in the body, not in the
	// Content-Type (a bare application/json): the scanner checks each kind
	// before anything of an item is kept.
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		_ = resp.Body.Close()
		return nil, &StatusError{Code: http.StatusNotAcceptable, Reason: "NotAcceptable", Message: "the server answered with " + ct}
	}
	return resp.Body, nil
}

// ListPage reads one page and applies each item before decoding the next.
// It returns the list's resourceVersion and continue token.
func (r *Reader) ListPage(ctx context.Context, gvr schema.GroupVersionResource, cont string, apply func(Source) error) (rv, next string, err error) {
	q := url.Values{"limit": {strconv.Itoa(PageSize)}}
	if cont != "" {
		q.Set("continue", cont)
	}
	body, err := r.get(ctx, gvr, q, acceptList)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = body.Close() }()
	sc := newScanner(body)
	isList := false
	err = sc.fields(func(key string) error {
		switch key {
		case "kind":
			kind, _, err := sc.str(keyBytes)
			if err == nil && kind != "PartialObjectMetadataList" {
				return notMetadata(kind)
			}
			isList = err == nil
			return err
		case "metadata":
			return sc.fields(func(key string) (err error) {
				switch key {
				case "resourceVersion":
					rv, err = sc.kept()
				case "continue":
					var over bool
					if next, over, err = sc.str(continueBytes); err == nil && over {
						err = ErrOversize
					}
				default:
					err = sc.skip()
				}
				return err
			})
		case "items":
			if !isList {
				return notMetadata("a list that does not name its kind before its items")
			}
			return sc.elements(func() error {
				var it scanned
				if err := sc.object(&it); err != nil {
					return err
				}
				if it.kind != "PartialObjectMetadata" {
					return notMetadata(orNone(it.kind))
				}
				return apply(it.src)
			})
		default:
			return sc.skip()
		}
	})
	if err != nil {
		return "", "", err
	}
	return rv, next, nil
}

func orNone(kind string) string {
	if kind == "" {
		return "an object without a kind"
	}
	return kind
}

func notMetadata(kind string) error {
	return &StatusError{Code: http.StatusNotAcceptable, Reason: "NotAcceptable", Message: "the server answered with " + kind}
}

// Event types a watch applies.
const (
	EvAdded    = "ADDED"
	EvModified = "MODIFIED"
	EvDeleted  = "DELETED"
	EvBookmark = "BOOKMARK"
)

// Watch reads one watch from rv, applying each event before decoding the
// next, until the server closes it (nil), ctx ends, or it fails. It returns
// the last resourceVersion seen, so a closed watch resumes from it.
func (r *Reader) Watch(ctx context.Context, gvr schema.GroupVersionResource, rv string, timeout time.Duration, apply func(ev string, s Source) error) (string, error) {
	q := url.Values{"watch": {"1"}, "resourceVersion": {rv}, "allowWatchBookmarks": {"true"}, "timeoutSeconds": {strconv.Itoa(int(timeout / time.Second))}}
	body, err := r.get(ctx, gvr, q, acceptWatch)
	if err != nil {
		return rv, err
	}
	defer func() { _ = body.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = body.Close() })
	defer stop()
	sc := newScanner(body)
	for {
		if eof, err := sc.atEOF(); err != nil || eof {
			if ctx.Err() != nil {
				return rv, ctx.Err()
			}
			return rv, err
		}
		var typ string
		var obj scanned
		err := sc.fields(func(key string) (err error) {
			switch key {
			case "type":
				typ, _, err = sc.str(keyBytes)
			case "object":
				err = sc.object(&obj)
			default:
				err = sc.skip()
			}
			return err
		})
		if err != nil {
			if ctx.Err() != nil {
				return rv, ctx.Err()
			}
			return rv, err
		}
		switch typ {
		case "ERROR":
			return rv, &StatusError{Code: obj.code, Reason: obj.reason, Message: obj.message}
		case EvBookmark:
			if obj.rv != "" {
				rv = obj.rv
			}
			continue
		case EvAdded, EvModified, EvDeleted:
		default:
			continue
		}
		if obj.kind != "PartialObjectMetadata" {
			return rv, notMetadata(orNone(obj.kind))
		}
		if err := apply(typ, obj.src); err != nil {
			return rv, err
		}
		if obj.rv != "" {
			rv = obj.rv
		}
	}
}
