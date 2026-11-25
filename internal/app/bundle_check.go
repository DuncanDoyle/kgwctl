package app

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"strings"

	"github.com/DuncanDoyle/snellius/pkg/extension"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/rest"

	"github.com/DuncanDoyle/kgwctl/internal/load"
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
		if in.State == load.StateDenied {
			recordDenied(s, in)
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

// recordDenied records a denied check input as a typed 403 at
// check/<group>/<resource>, so it gets a denied item and a grant line (§6).
// Check reads cluster-wide, so the grant is cluster-wide. A cache
// pseudo-resource such as pods#proxy is written pods-proxy.
func recordDenied(s pb.Sink, in load.Input) {
	group := cmp.Or(in.GVR.Group, pb.Core)
	res := strings.ReplaceAll(in.GVR.Resource, "#", "-")
	err := apierrors.NewForbidden(in.GVR.GroupResource(), "", errors.New("check input denied"))
	s.Error(pb.Path{pb.CheckDir, group, res}, pb.Source{Operation: pb.OpCheck}, err)
}
