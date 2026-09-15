package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type stubReady struct{ ready bool }

func (s *stubReady) Ready() bool { return s.ready }

func TestHealthzAlwaysOK(t *testing.T) {
	srv := httptest.NewServer(newMux("/metrics", nil, &stubReady{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
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
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 before ready", resp.StatusCode)
	}

	r.ready = true
	resp, err = http.Get(srv.URL + "/readyz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 once ready", resp.StatusCode)
	}
}
