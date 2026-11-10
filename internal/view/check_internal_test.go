package view

import (
	"testing"
	"time"

	"github.com/DuncanDoyle/snellius/pkg/diagnose"
	"github.com/DuncanDoyle/snellius/pkg/objects"
)

// The kube context is quoted as one shell word on every kubectl line; other lines stay intact.
func TestContextCommandsQuoting(t *testing.T) {
	pod := objects.Ref{Kind: "Pod", Namespace: "ns", Name: "p"}
	for _, tc := range []struct{ ctx, want string }{
		{"prod", "prod"},
		{"my ctx", "'my ctx'"},
		{"it's", `'it'\''s'`},
		{"$(rm -rf x)", "'$(rm -rf x)'"},
		{"a\nb", "'a\nb'"},
	} {
		c := contextCommands{diagnose.Kubectl{}, tc.ctx}
		if got, want := c.ControllerLog(pod, time.Hour, "x"), "kubectl --context "+tc.want+" logs -n ns p --since=1h | grep x"; got != want {
			t.Errorf("ctx %q:\n got %q\nwant %q", tc.ctx, got, want)
		}
		got := c.ProxyCounters(objects.Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "px"})
		want := "kubectl --context " + tc.want + " port-forward -n ns deploy/px 19000:19000\n" +
			"curl -s localhost:19000/stats | grep -E 'update_(rejected|success)'"
		if got != want {
			t.Errorf("ctx %q:\n got %q\nwant %q", tc.ctx, got, want)
		}
	}
}
