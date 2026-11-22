package redact

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// maxErrorText is the cut for ErrorText, design §7.1.
const maxErrorText = 512

// streamStatusRe is client-go's error for a non-2xx stream with an unparsed body.
var streamStatusRe = regexp.MustCompile(`\b([1-5][0-9]{2}) while accessing `)

// RemoteError renders an error from a remote endpoint as its HTTP status and
// Kubernetes reason only, such as "403 Forbidden (Forbidden)" (design §7.1).
// Messages and response bodies never pass. An error with no status gives a
// fixed text; the caller adds the operation.
func RemoteError(err error) string {
	if err == nil {
		return ""
	}
	var st apierrors.APIStatus
	if errors.As(err, &st) {
		s := st.Status()
		return statusText(int(s.Code), string(s.Reason))
	}
	if m := streamStatusRe.FindStringSubmatch(err.Error()); m != nil {
		code, _ := strconv.Atoi(m[1])
		return statusText(code, "")
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "context deadline exceeded"
	case errors.Is(err, context.Canceled):
		return "context canceled"
	}
	return "request failed"
}

// reasons are the canonical Kubernetes StatusReasons; others are not shown.
var reasons = map[metav1.StatusReason]bool{
	metav1.StatusReasonUnauthorized: true, metav1.StatusReasonForbidden: true, metav1.StatusReasonNotFound: true,
	metav1.StatusReasonAlreadyExists: true, metav1.StatusReasonConflict: true, metav1.StatusReasonGone: true,
	metav1.StatusReasonInvalid: true, metav1.StatusReasonServerTimeout: true, metav1.StatusReasonStoreReadError: true,
	metav1.StatusReasonTimeout: true, metav1.StatusReasonTooManyRequests: true, metav1.StatusReasonBadRequest: true,
	metav1.StatusReasonMethodNotAllowed: true, metav1.StatusReasonNotAcceptable: true,
	metav1.StatusReasonRequestEntityTooLarge: true, metav1.StatusReasonUnsupportedMediaType: true,
	metav1.StatusReasonInternalError: true, metav1.StatusReasonExpired: true, metav1.StatusReasonServiceUnavailable: true,
}

// statusText renders a status code and reason. A reason that is not
// canonical gives "request failed (<code>)".
func statusText(code int, reason string) string {
	if code < 100 || code > 599 {
		code = 0
	}
	if reason != "" && !reasons[metav1.StatusReason(reason)] {
		if code == 0 {
			return "request failed"
		}
		return fmt.Sprintf("request failed (%d)", code)
	}
	var parts []string
	if code != 0 {
		parts = append(parts, strings.TrimSpace(fmt.Sprintf("%d %s", code, http.StatusText(code))))
	}
	if reason != "" {
		parts = append(parts, "("+reason+")")
	}
	if len(parts) == 0 {
		return "request failed"
	}
	return strings.Join(parts, " ")
}

// ErrorText renders any other error, a panic value included, for the
// manifest, errors.txt, run.log and stderr (design §7.1): view.Redactor for
// server, the pattern pass, one line, cut at 512 bytes.
func ErrorText(err error, server string) string {
	if err == nil {
		return ""
	}
	s := view.NewRedactor(server).Redact(err.Error())
	s, _ = patternPass(s)
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
	if len(s) <= maxErrorText {
		return s
	}
	i := maxErrorText
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i]
}

// IsRemote reports whether err came from a remote endpoint.
func IsRemote(err error) bool {
	var st apierrors.APIStatus
	return err != nil && (errors.As(err, &st) || streamStatusRe.MatchString(err.Error()))
}
