package proxydump

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TestOldestTieByName: equal start times pick the first name, whatever the
// input order, so the choice is stable across List orderings.
func TestOldestTieByName(t *testing.T) {
	started := metav1.NewTime(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	pod := func(name string) corev1.Pod {
		return corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.PodStatus{StartTime: &started}}
	}
	if got := oldest([]corev1.Pod{pod("tie-c"), pod("tie-b"), pod("tie-a")}).Name; got != "tie-a" {
		t.Fatalf("oldest = %s, want tie-a", got)
	}
}
