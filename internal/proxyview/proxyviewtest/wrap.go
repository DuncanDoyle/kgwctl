// Package proxyviewtest holds test helpers for proxy routes: a module that
// wraps another under its own name, as a downstream build does.
package proxyviewtest

import (
	"github.com/DuncanDoyle/xdsview/pkg/envoy/model"
	"github.com/DuncanDoyle/xdsview/pkg/envoy/provenance"
)

// Wrap returns a module named name that re-emits inner's claims, then the
// claims extra adds; extra may be nil.
func Wrap(name string, inner provenance.Module, extra func(snap *model.Snapshot, inner []provenance.Claim) []provenance.Claim) provenance.Module {
	return wrapper{name: name, inner: inner, extra: extra}
}

type wrapper struct {
	name  string
	inner provenance.Module
	extra func(*model.Snapshot, []provenance.Claim) []provenance.Claim
}

func (w wrapper) Name() string { return w.name }

func (w wrapper) Claims(snap *model.Snapshot) []provenance.Claim {
	claims := w.inner.Claims(snap)
	if w.extra == nil {
		return claims
	}
	return append(claims, w.extra(snap, claims)...)
}
