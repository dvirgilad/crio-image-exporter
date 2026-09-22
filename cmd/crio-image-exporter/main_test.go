package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubReady struct{ ready bool }

func (s *stubReady) Ready() bool { return s.ready }

// Kubelet runs HTTP probes from the node's network namespace and cannot reach
// a loopback-bound listener, so the chart uses an exec probe that re-runs this
// binary with --healthcheck inside the container. These cover that path.
func TestHealthcheckURLUsesLoopbackAndListenPort(t *testing.T) {
	for _, tc := range []struct{ listen, path, want string }{
		{"127.0.0.1:8080", "/readyz", "http://127.0.0.1:8080/readyz"},
		{"0.0.0.0:9100", "/readyz", "http://127.0.0.1:9100/readyz"},
		{":8080", "/readyz", "http://127.0.0.1:8080/readyz"},
		// Liveness must be able to target /healthz: it is not gated on CRI, so
		// pointing liveness at /readyz would restart the pod on slow CRI start.
		{"127.0.0.1:8080", "/healthz", "http://127.0.0.1:8080/healthz"},
		{"127.0.0.1:8080", "", "http://127.0.0.1:8080/readyz"},
	} {
		if got := healthcheckURL(tc.listen, tc.path); got != tc.want {
			t.Errorf("healthcheckURL(%q,%q) = %q, want %q", tc.listen, tc.path, got, tc.want)
		}
	}
}

func TestReadyAtReflectsStatus(t *testing.T) {
	ready := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ready.Close()
	notReady := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer notReady.Close()

	if !readyAt(ready.URL) {
		t.Error("readyAt should be true for 200")
	}
	if readyAt(notReady.URL) {
		t.Error("readyAt should be false for 503")
	}
	// An unreachable exporter must fail the probe, not hang or panic.
	if readyAt("http://127.0.0.1:1/readyz") {
		t.Error("readyAt should be false when nothing is listening")
	}
}

func TestHealthzAlwaysOK(t *testing.T) {
	srv := httptest.NewServer(newMux("/metrics", nil, &stubReady{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestReadyzReflectsReadiness(t *testing.T) {
	r := &stubReady{}
	srv := httptest.NewServer(newMux("/metrics", nil, r))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 before ready", resp.StatusCode)
	}

	r.ready = true
	resp, err = http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 once ready", resp.StatusCode)
	}
}
