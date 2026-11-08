package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/panos/audio-briefing/internal/briefing"
	"github.com/panos/audio-briefing/internal/config"
)

func newTestServer(t *testing.T) (*Server, http.Handler, string) {
	t.Helper()
	base := t.TempDir()
	cfg := &config.Config{
		BaseDir:      base,
		AuthUsername: "admin",
		AuthPassword: "secret",
	}
	gen := &briefing.Generator{Cfg: cfg}
	srv := New(cfg, gen)
	h, err := srv.Handler()
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return srv, h, base
}

func TestSecurityHeadersAndAuth(t *testing.T) {
	_, h, _ := newTestServer(t)

	// Unauthenticated /api/status -> 401
	req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing X-Content-Type-Options: nosniff")
	}
	if rec.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("missing X-Frame-Options: DENY")
	}

	// Unauthenticated /healthz -> 200
	reqHealth := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recHealth := httptest.NewRecorder()
	h.ServeHTTP(recHealth, reqHealth)
	if recHealth.Code != http.StatusOK {
		t.Fatalf("expected 200 on /healthz, got %d", recHealth.Code)
	}
}

func TestPathTraversalAndValidation(t *testing.T) {
	_, h, base := newTestServer(t)

	// Create a sibling directory to test prefix collision (e.g. base + "-evil")
	sibling := base + "-evil"
	_ = os.MkdirAll(sibling, 0o755)
	defer os.RemoveAll(sibling)
	_ = os.WriteFile(filepath.Join(sibling, "secret.mp3"), []byte("nope"), 0o644)

	// 1. Invalid date / slot on /api/briefing -> 400
	req := httptest.NewRequest(http.MethodGet, "/api/briefing?date=..&slot=..%2Fetc", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 on invalid date/slot, got %d", rec.Code)
	}

	// 2. Sibling traversal on /audio/ -> 403 or 404 (never 200)
	reqAudio := httptest.NewRequest(http.MethodGet, "/audio/../"+filepath.Base(sibling)+"/secret.mp3", nil)
	reqAudio.SetBasicAuth("admin", "secret")
	recAudio := httptest.NewRecorder()
	h.ServeHTTP(recAudio, reqAudio)
	if recAudio.Code == http.StatusOK {
		t.Fatalf("expected sibling traversal to be blocked, got 200")
	}
}

func TestGenerateCSRFProtection(t *testing.T) {
	_, h, _ := newTestServer(t)

	// Missing X-Requested-With / Sec-Fetch-Site -> 403
	req := httptest.NewRequest(http.MethodPost, "/api/generate?slot=morning", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without CSRF header, got %d", rec.Code)
	}

	// Mismatched Origin -> 403
	reqBadOrigin := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8088/api/generate?slot=morning", nil)
	reqBadOrigin.SetBasicAuth("admin", "secret")
	reqBadOrigin.Header.Set("X-Requested-With", "fetch")
	reqBadOrigin.Header.Set("Origin", "https://evil.example.com")
	recBadOrigin := httptest.NewRecorder()
	h.ServeHTTP(recBadOrigin, reqBadOrigin)
	if recBadOrigin.Code != http.StatusForbidden {
		t.Fatalf("expected 403 on cross-origin POST, got %d", recBadOrigin.Code)
	}
}
