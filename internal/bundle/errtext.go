package bundle

import (
	"context"
	"crypto/sha256"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// maxErrCache bounds the error text cache.
const maxErrCache = 4096

// errorText renders err for the manifest and errors.txt: a remote error as
// its status only, anything else through the error text redactor, which
// redacts the whole text before it cuts.
func (w *Writer) errorText(err error) string {
	if redact.IsRemote(err) {
		return redact.RemoteError(err)
	}
	msg := err.Error()
	key := sha256.Sum256([]byte(msg))
	w.errMu.Lock()
	s, ok := w.errCache[key]
	w.errMu.Unlock()
	if ok {
		return s
	}
	s = redact.ErrorText(errors.New(msg), w.server)
	w.errMu.Lock()
	if w.errCache == nil {
		w.errCache = map[[sha256.Size]byte]string{}
	}
	if len(w.errCache) < maxErrCache {
		w.errCache[key] = s
	}
	w.errMu.Unlock()
	return s
}

// ItemStatus is the item status the Sink records for err: denied on a 403,
// timeout on a deadline or timeout status, else failed.
func ItemStatus(err error) pb.ItemStatus {
	switch {
	case apierrors.IsForbidden(err):
		return pb.ItemDenied
	case errors.Is(err, context.DeadlineExceeded), apierrors.IsTimeout(err), apierrors.IsServerTimeout(err):
		return pb.ItemTimeout
	}
	return pb.ItemFailed
}
