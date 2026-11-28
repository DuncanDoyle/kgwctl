package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"

	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// CauseConnectionRefused is the StatusCause type the guard sets on a
// pods/proxy 502 or 503 whose body said the connection was refused.
const CauseConnectionRefused metav1.CauseType = "kgwctl.kgateway.dev/ConnectionRefused"

// refusedScan bounds how much of a pods/proxy error body the guard reads.
const refusedScan = 4 << 10

// refusedPhrase marks a refused connection in a pods/proxy error body.
var refusedPhrase = []byte("connect: connection refused")

// Guard returns a copy of cfg whose transport refuses every request for core
// secrets and configmaps with pb.ErrForbiddenKind, unsent, and replaces each
// 3xx, 4xx and 5xx response body with a Status holding only code, reason and
// the refused cause (design §6.1, §7.1). Every 3xx also loses its Location, so
// no redirect is followed and the client gets a status-only failure. Request
// paths are resolved below the server URL's path. Bodies are never stored. Guarding a guarded config is
// safe: the inner guard's body and cause pass through.
func Guard(cfg *rest.Config) *rest.Config {
	c := rest.CopyConfig(cfg)
	var base string
	if u, _, err := rest.DefaultServerUrlFor(c); err == nil {
		base = strings.Trim(path.Clean("/"+u.Path), "/")
	}
	c.Wrap(func(rt http.RoundTripper) http.RoundTripper { return &guard{next: rt, base: base} })
	return c
}

type guard struct {
	next http.RoundTripper
	// base is the server URL's path without slashes; client-go prefixes it to every request.
	base string
}

// guardedBody is a body the guard wrote; a server cannot forge its type, so
// an outer guard passes it through and Guard is idempotent.
type guardedBody struct{ *bytes.Reader }

func (guardedBody) Close() error { return nil }

// WrappedRoundTripper lets client-go find the inner transport.
func (g *guard) WrappedRoundTripper() http.RoundTripper { return g.next }

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	res, sub := resolve(req.URL.Path, g.base)
	if forbidden(res) {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("%w: %s", pb.ErrForbiddenKind, res)
	}
	resp, err := g.next.RoundTrip(req)
	// 1xx (upgrades) and 2xx pass; 3xx, 4xx and 5xx bodies are replaced.
	if err != nil || resp.StatusCode < 300 {
		return resp, err
	}
	// An inner guard already replaced the body; keep its refused cause.
	if _, ok := resp.Body.(guardedBody); ok {
		return resp, nil
	}
	var refused bool
	if res == "pods" && sub == "proxy" && (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable) {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, refusedScan))
		refused = bytes.Contains(head, refusedPhrase)
	}
	_ = resp.Body.Close()
	body := statusBody(resp.StatusCode, refused)
	resp.Body = guardedBody{bytes.NewReader(body)}
	resp.ContentLength = int64(len(body))
	// The server's reason phrase is text too; keep only the standard one.
	resp.Status = strings.TrimSpace(fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode)))
	resp.Header = resp.Header.Clone()
	if resp.Header == nil {
		resp.Header = http.Header{}
	}
	resp.Header.Del("Content-Encoding")
	// Bundle clients never follow redirects; net/http would quote Location in its errors.
	resp.Header.Del("Location")
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return resp, nil
}

// forbidden reports whether res is a core resource the bundle never requests.
func forbidden(res string) bool { return res == "secrets" || res == "configmaps" }

// resolve returns the core resource and subresource of path below base. A path
// outside base is refused if any api/v1 in it names a forbidden resource.
func resolve(path, base string) (resource, sub string) {
	p := strings.Trim(path, "/")
	if base == "" || p == base || strings.HasPrefix(p, base+"/") {
		return coreResource(strings.Split(strings.TrimPrefix(strings.TrimPrefix(p, base), "/"), "/"))
	}
	segs := strings.Split(p, "/")
	for i := range segs {
		if r, s := coreResource(segs[i:]); forbidden(r) {
			return r, s
		}
	}
	return coreResource(segs)
}

// coreResource returns the core API resource of a path that starts at api/v1
// and, for a named namespaced object, its subresource; "" for any other path.
func coreResource(segs []string) (resource, sub string) {
	if len(segs) < 2 || segs[0] != "api" || segs[1] != "v1" {
		return "", ""
	}
	tail := segs[2:]
	if len(tail) > 0 && tail[0] == "watch" {
		tail = tail[1:]
	}
	// namespaces/<ns>/<resource>[/<name>[/<sub>]]; namespaces/<name> is a Namespace.
	if len(tail) >= 3 && tail[0] == "namespaces" {
		tail = tail[2:]
	}
	if len(tail) == 0 {
		return "", ""
	}
	if len(tail) >= 3 {
		sub = tail[2]
	}
	return tail[0], sub
}

// statusBody is the canonical Status for code: reason from the code alone.
func statusBody(code int, refused bool) []byte {
	st := metav1.Status{
		TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
		Status:   metav1.StatusFailure,
		Code:     int32(code),
		Reason:   reasonFor(code),
		Message:  strings.TrimSpace(fmt.Sprintf("%d %s", code, http.StatusText(code))),
	}
	if refused {
		st.Details = &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: CauseConnectionRefused}}}
	}
	b, _ := json.Marshal(st)
	return b
}

// reasonFor maps an HTTP status to its canonical StatusReason; unknown otherwise.
func reasonFor(code int) metav1.StatusReason {
	switch code {
	case http.StatusBadRequest:
		return metav1.StatusReasonBadRequest
	case http.StatusUnauthorized:
		return metav1.StatusReasonUnauthorized
	case http.StatusForbidden:
		return metav1.StatusReasonForbidden
	case http.StatusNotFound:
		return metav1.StatusReasonNotFound
	case http.StatusMethodNotAllowed:
		return metav1.StatusReasonMethodNotAllowed
	case http.StatusNotAcceptable:
		return metav1.StatusReasonNotAcceptable
	case http.StatusConflict:
		return metav1.StatusReasonConflict
	case http.StatusGone:
		return metav1.StatusReasonGone
	case http.StatusRequestEntityTooLarge:
		return metav1.StatusReasonRequestEntityTooLarge
	case http.StatusUnsupportedMediaType:
		return metav1.StatusReasonUnsupportedMediaType
	case http.StatusUnprocessableEntity:
		return metav1.StatusReasonInvalid
	case http.StatusTooManyRequests:
		return metav1.StatusReasonTooManyRequests
	case http.StatusInternalServerError:
		return metav1.StatusReasonInternalError
	case http.StatusServiceUnavailable:
		return metav1.StatusReasonServiceUnavailable
	case http.StatusGatewayTimeout:
		return metav1.StatusReasonTimeout
	}
	return metav1.StatusReasonUnknown
}

// Refused reports whether err carries the guard's connection refused cause.
func Refused(err error) bool {
	var st apierrors.APIStatus
	if !errors.As(err, &st) || st.Status().Details == nil {
		return false
	}
	for _, c := range st.Status().Details.Causes {
		if c.Type == CauseConnectionRefused {
			return true
		}
	}
	return false
}
