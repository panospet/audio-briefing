package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestEmbeddedWebFrontend(t *testing.T) {
	_, h, _ := newTestServer(t)

	for _, path := range []string{"/", "/static/style.css", "/static/player.js", "/static/assets/signal-hero.svg", "/static/assets/sonic-core.svg", "/static/assets/favicon.svg"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.SetBasicAuth("admin", "secret")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", path, rec.Code)
		}
		if rec.Body.Len() == 0 {
			t.Fatalf("expected non-empty response for %s", path)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	for _, expected := range []string{"NŌEMA", "/static/assets/signal-hero.svg", "/static/assets/sonic-core.svg", `id="audio"`} {
		if !strings.Contains(body, expected) {
			t.Errorf("expected embedded index to contain %q", expected)
		}
	}
}

func TestPodcastRoutes(t *testing.T) {
	_, h, base := newTestServer(t)

	// /podcast serves the same embedded SPA and must expose the header button.
	req := httptest.NewRequest(http.MethodGet, "/podcast", nil)
	req.SetBasicAuth("admin", "secret")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for /podcast, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `id="podcast-link"`) {
		t.Fatalf("expected /podcast page to contain the podcast link")
	}

	// /api/podcast without a published podcast -> 404
	reqMissing := httptest.NewRequest(http.MethodGet, "/api/podcast", nil)
	reqMissing.SetBasicAuth("admin", "secret")
	recMissing := httptest.NewRecorder()
	h.ServeHTTP(recMissing, reqMissing)
	if recMissing.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing podcast, got %d", recMissing.Code)
	}

	// Stage a podcast edition and expect it to be served, along with its audio.
	podDir := filepath.Join(base, "podcasts", "edition-a")
	if err := os.MkdirAll(podDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	playlist := `{"podcast":true,"title":"Test Edition","date":"2026-11-14","default":"all",` +
		`"genres":[{"id":"all","label":"Full Guide"}],` +
		`"variants":{"all":[{"id":"n0","type":"news","title":"Hello","subtitle":"Intro",` +
		`"url":"/audio/podcasts/edition-a/segments/n0.mp3","duration":3.5,` +
		`"sources":[{"newspaper":"rocking.gr","title":"Guide","url":"https://example.com"}]}]},` +
		`"items":[{"id":"n0","type":"news","title":"Hello","subtitle":"Intro",` +
		`"url":"/audio/podcasts/edition-a/segments/n0.mp3","duration":3.5,` +
		`"sources":[{"newspaper":"rocking.gr","title":"Guide","url":"https://example.com"}]}]}`
	if err := os.WriteFile(filepath.Join(podDir, "playlist.json"), []byte(playlist), 0o644); err != nil {
		t.Fatalf("write playlist: %v", err)
	}
	segDir := filepath.Join(podDir, "segments")
	if err := os.MkdirAll(segDir, 0o755); err != nil {
		t.Fatalf("mkdir segments: %v", err)
	}
	if err := os.WriteFile(filepath.Join(segDir, "n0.mp3"), []byte("audio-bytes"), 0o644); err != nil {
		t.Fatalf("write segment: %v", err)
	}

	reqPod := httptest.NewRequest(http.MethodGet, "/api/podcast", nil)
	reqPod.SetBasicAuth("admin", "secret")
	recPod := httptest.NewRecorder()
	h.ServeHTTP(recPod, reqPod)
	if recPod.Code != http.StatusOK {
		t.Fatalf("expected 200 for published podcast, got %d", recPod.Code)
	}
	if !strings.Contains(recPod.Body.String(), `"Test Edition"`) {
		t.Fatalf("expected podcast playlist body, got: %s", recPod.Body.String())
	}

	reqAudio := httptest.NewRequest(http.MethodGet, "/audio/podcasts/edition-a/segments/n0.mp3", nil)
	reqAudio.SetBasicAuth("admin", "secret")
	recAudio := httptest.NewRecorder()
	h.ServeHTTP(recAudio, reqAudio)
	if recAudio.Code != http.StatusOK || recAudio.Body.String() != "audio-bytes" {
		t.Fatalf("expected staged podcast audio, got %d %q", recAudio.Code, recAudio.Body.String())
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
