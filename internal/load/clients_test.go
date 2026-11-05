package load_test

import (
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/DuncanDoyle/kgwctl/internal/load"
)

func TestNewClientsThrottleDefaults(t *testing.T) {
	for _, tc := range []struct {
		name             string
		qps, wantQPS     float32
		burst, wantBurst int
	}{
		{"unset gets kubectl defaults", 0, 50, 0, 300},
		{"explicit values kept", 7, 7, 11, 11},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &rest.Config{Host: "https://127.0.0.1:1", QPS: tc.qps, Burst: tc.burst}
			c, err := load.NewClients(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			// The rate limiter exposes QPS only; Burst is checked on the defaulted config.
			if got := c.Kube.(*kubernetes.Clientset).CoreV1().RESTClient().GetRateLimiter().QPS(); got != tc.wantQPS {
				t.Errorf("client QPS = %v, want %v", got, tc.wantQPS)
			}
			d := load.ThrottleDefaults(&rest.Config{QPS: tc.qps, Burst: tc.burst})
			if d.QPS != tc.wantQPS || d.Burst != tc.wantBurst {
				t.Errorf("defaults = QPS %v Burst %d, want %v %d", d.QPS, d.Burst, tc.wantQPS, tc.wantBurst)
			}
			if cfg.QPS != tc.qps || cfg.Burst != tc.burst {
				t.Errorf("caller's config changed to QPS %v Burst %d", cfg.QPS, cfg.Burst)
			}
		})
	}
}
