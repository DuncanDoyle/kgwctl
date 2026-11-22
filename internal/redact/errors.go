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

	"github.com/DuncanDoyle/kgwctl/internal/view"
)

// maxErrorText is the cut for ErrorText, design §7.1.
const maxErrorText = 512

var (
	// streamStatusRe is client-go's error for a non-2xx stream with an unparsed body.
	streamStatusRe = regexp.MustCompile(`\b([1-5][0-9]{2}) while accessing `)
	// reasonRe is a Kubernetes StatusReason; anything else is left out.
	reasonRe = regexp.MustCompile(`^[A-Za-z]{1,64}$`)
)

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

func statusText(code int, reason string) string {
	var parts []string
	if code != 0 {
		parts = append(parts, strings.TrimSpace(fmt.Sprintf("%d %s", code, http.StatusText(code))))
	}
	if reasonRe.MatchString(reason) {
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
