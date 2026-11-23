package collect

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	ib "github.com/DuncanDoyle/kgwctl/internal/bundle"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// Scraper fetches the /metrics body of one Pod on port; the caller closes it.
type Scraper interface {
	Scrape(ctx context.Context, pod *corev1.Pod, port int32) (io.ReadCloser, error)
}

// HTTPStatusError is a non-200 metrics response: its code, canonical reason
// and whether the guard saw a refused connection. It never holds body text.
type HTTPStatusError struct {
	Code    int
	Reason  string
	Refused bool
}

// Status implements apierrors.APIStatus, so a 403 is recorded as denied and
// ib.Refused sees the refused cause.
func (e *HTTPStatusError) Status() metav1.Status {
	st := metav1.Status{Status: metav1.StatusFailure, Code: int32(e.Code), Reason: metav1.StatusReason(e.Reason)}
	if e.Refused {
		st.Details = &metav1.StatusDetails{Causes: []metav1.StatusCause{{Type: ib.CauseConnectionRefused}}}
	}
	return st
}

// Error renders the status only (design §7.1).
func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("%s: %s", pb.OpMetrics, redact.RemoteError(e))
}

// statusScan bounds how much of the guard's Status body is decoded.
const statusScan = 4 << 10

type statusScraper struct{ rc *rest.RESTClient }

// StatusScraper scrapes through pods/proxy with one GET and no redirects.
// cfg must be guarded (ib.Guard); a non-200 is an *HTTPStatusError.
func StatusScraper(cfg *rest.Config) (Scraper, error) {
	kube, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	rc, ok := kube.CoreV1().RESTClient().(*rest.RESTClient)
	if !ok || rc == nil || rc.Client == nil {
		return nil, errors.New("pods/proxy unavailable")
	}
	return statusScraper{rc}, nil
}

// Scrape implements Scraper. Request.Stream is not used: it retries on
// Retry-After and reads error bodies unbounded.
func (s statusScraper) Scrape(ctx context.Context, pod *corev1.Pod, port int32) (io.ReadCloser, error) {
	u := s.rc.Get().Namespace(pod.Namespace).Resource("pods").Name("http:" + pod.Name + ":" + strconv.Itoa(int(port))).
		SubResource("proxy").Suffix("metrics").URL()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%s: request failed", pb.OpMetrics)
	}
	req.Header.Set("Accept", "text/plain;version=0.0.4")
	c := *s.rc.Client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := c.Do(req)
	if err != nil {
		// Transport errors name the host and Pod; only context errors pass.
		return nil, logError(pb.OpMetrics, err)
	}
	if resp.StatusCode == http.StatusOK {
		return resp.Body, nil
	}
	defer resp.Body.Close()
	he := &HTTPStatusError{Code: resp.StatusCode}
	// The guard's body is a Status with the canonical reason and the refused cause.
	var st metav1.Status
	if resp.StatusCode >= 400 {
		if b, _ := io.ReadAll(io.LimitReader(resp.Body, statusScan)); json.Unmarshal(b, &st) == nil {
			he.Reason = string(st.Reason)
			he.Refused = ib.Refused(&apierrors.StatusError{ErrStatus: st})
		}
	}
	return nil, he
}
