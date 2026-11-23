package bundle

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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
// 4xx and 5xx response body with a Status holding only code, reason and the
// refused cause (design §6.1, §7.1). Bodies are never stored.
func Guard(cfg *rest.Config) *rest.Config {
	c := rest.CopyConfig(cfg)
	c.Wrap(func(rt http.RoundTripper) http.RoundTripper { return &guard{next: rt} })
	return c
}

type guard struct{ next http.RoundTripper }

// WrappedRoundTripper lets client-go find the inner transport.
func (g *guard) WrappedRoundTripper() http.RoundTripper { return g.next }

func (g *guard) RoundTrip(req *http.Request) (*http.Response, error) {
	res, sub := coreResource(req.URL.Path)
	if res == "secrets" || res == "configmaps" {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, fmt.Errorf("%w: %s", pb.ErrForbiddenKind, res)
	}
	resp, err := g.next.RoundTrip(req)
	// 1xx (upgrades), 2xx and 3xx pass; 4xx and 5xx bodies are replaced.
	if err != nil || resp.StatusCode < 400 {
		return resp, err
	}
	var refused bool
	if res == "pods" && sub == "proxy" && (resp.StatusCode == http.StatusBadGateway || resp.StatusCode == http.StatusServiceUnavailable) {
		head, _ := io.ReadAll(io.LimitReader(resp.Body, refusedScan))
		refused = bytes.Contains(head, refusedPhrase)
	}
	_ = resp.Body.Close()
	body := statusBody(resp.StatusCode, refused)
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header = resp.Header.Clone()
	if resp.Header == nil {
		resp.Header = http.Header{}
	}
	resp.Header.Del("Content-Encoding")
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return resp, nil
}

// coreResource returns the core API resource of path and, for a named
// namespaced object, its subresource; "" for any other path.
func coreResource(path string) (resource, sub string) {
	segs := strings.Split(strings.Trim(path, "/"), "/")
	i := 0
	for i+1 < len(segs) && (segs[i] != "api" || segs[i+1] != "v1") {
		i++
	}
	if i+1 >= len(segs) {
		return "", ""
	}
	tail := segs[i+2:]
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
