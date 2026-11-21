package proxydump

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"k8s.io/client-go/rest"
)

// TestPortForwardRefusesRedirect: a redirected upgrade is an error and the
// redirect target never sees a request, so no credentials follow it.
func TestPortForwardRefusesRedirect(t *testing.T) {
	var elsewhere atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	t.Cleanup(second.Close)
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.URL+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)

	cfg := &rest.Config{Host: first.URL, BearerToken: "secret-token"}
	_, _, err := NewPortForwarder(cfg).Dial(t.Context(), testPod, AdminPort)
	if err == nil || !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("err = %v, want a redirect error", err)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Fatalf("redirect target got %d requests, want 0", n)
	}
}

// TestDialViaUnregistersOnClose: closing the socket set also drops each
// socket's ctx callback, so a long-lived ctx keeps no closed socket alive.
func TestDialViaUnregistersOnClose(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			t.Cleanup(func() { _ = c.Close() })
		}
	}()

	conns := &connSet{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := dialVia(ctx, nil, nil, "tcp", ln.Addr().String(), conns); err != nil {
		t.Fatal(err)
	}
	conns.mu.Lock()
	if len(conns.socks) != 1 {
		conns.mu.Unlock()
		t.Fatalf("socket set holds %d sockets, want 1", len(conns.socks))
	}
	unregister := conns.socks[0].stopAfter
	conns.mu.Unlock()

	conns.closeAll()
	if unregister() {
		t.Fatal("the socket's ctx callback was still registered after closeAll")
	}
}
