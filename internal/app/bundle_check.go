package app

import (
	"bytes"
	"context"
	"errors"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	"k8s.io/client-go/rest"

	"github.com/DuncanDoyle/kgwctl/internal/print"
	"github.com/DuncanDoyle/kgwctl/internal/redact"
	pb "github.com/DuncanDoyle/kgwctl/pkg/bundle"
)

// checkCollector runs one check and writes its report as check/check.json.
type checkCollector struct {
	f     *Flags
	rc    *rest.Config
	reg   *extension.Registry
	build BuildInfo
	hooks checkTestHooks
}

// newCheckCollector returns the check collector (design §6): the path of
// `check -o json` with the run's flags over rc, which must be guarded.
func newCheckCollector(f *Flags, rc *rest.Config, reg *extension.Registry, build BuildInfo, hooks checkTestHooks) pb.Collector {
	return checkCollector{f: f, rc: rc, reg: reg, build: build, hooks: hooks}
}

// Name implements pb.Collector.
func (checkCollector) Name() string { return "check" }

// Collect implements pb.Collector. A report is written whatever its exit;
// without one the error is recorded.
func (c checkCollector) Collect(ctx context.Context, _ pb.Env, s pb.Sink) error {
	p := pb.Path{pb.CheckDir, pb.CheckFile}
	src := pb.Source{Operation: pb.OpCheck}
	report, err := checkOnce(ctx, c.f, c.rc, c.reg, c.build, c.hooks)
	var v *checkVerdict
	if err != nil && !errors.As(err, &v) {
		s.Error(p, src, err)
		return nil
	}
	// Reasons are redacted and cut like any other error text (§7.1).
	for i, in := range report.Incomplete {
		if in.Reason != "" {
			report.Incomplete[i].Reason = redact.ErrorText(errors.New(in.Reason), c.rc.Host)
		}
	}
	var b bytes.Buffer
	if err := print.Envelope(&b, print.FormatJSON, "Check", report); err != nil {
		s.Error(p, src, err)
		return nil
	}
	if err := s.JSON(ctx, p, &b, src); errors.Is(err, pb.ErrSealed) {
		return err
	}
	return nil
}
