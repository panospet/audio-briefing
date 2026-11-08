package server

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/panos/audio-briefing/internal/briefing"
	"github.com/panos/audio-briefing/internal/config"
)

//go:embed web
var webFS embed.FS

var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// Server is the HTTP server for the web player and JSON APIs.
type Server struct {
	Cfg     *config.Config
	Gen     *briefing.Generator
	BaseCtx context.Context

	httpSrv *http.Server
}

func New(cfg *config.Config, gen *briefing.Generator) *Server {
	return &Server{
		Cfg:     cfg,
		Gen:     gen,
		BaseCtx: context.Background(),
	}
}

// Handler builds the HTTP handler tree with security headers and Basic Auth.
func (s *Server) Handler() (http.Handler, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /api/briefings", s.handleBriefings)
	mux.HandleFunc("GET /api/briefing", s.handleBriefing)
	mux.HandleFunc("GET /api/latest", s.handleLatest)
	mux.HandleFunc("POST /api/generate", s.handleGenerate)
	mux.HandleFunc("GET /api/status", s.handleStatus)
	mux.HandleFunc("GET /audio/", s.handleAudio)

	staticSub, err := fs.Sub(webFS, "web")
	if err != nil {
		return nil, err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	return s.securityHeaders(s.auth(mux)), nil
}

// ListenAndServe builds the mux and serves on addr with timeouts configured.
func (s *Server) ListenAndServe(addr string) error {
	h, err := s.Handler()
	if err != nil {
		return err
	}
	s.httpSrv = &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpSrv == nil {
		return nil
	}
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; media-src 'self'; style-src 'self'; script-src 'self'; object-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if s.Cfg.AuthUsername == "" || s.Cfg.AuthPassword == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		u, p, ok := r.BasicAuth()
		uMatch := subtle.ConstantTimeCompare([]byte(u), []byte(s.Cfg.AuthUsername)) == 1
		pMatch := subtle.ConstantTimeCompare([]byte(p), []byte(s.Cfg.AuthPassword)) == 1
		if !ok || !uMatch || !pMatch {
			w.Header().Set("WWW-Authenticate", `Basic realm="briefing"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func isValidSlot(slot string) bool {
	return slot == "morning" || slot == "evening"
}

func (s *Server) isWithinBaseDir(fullPath string) bool {
	base := filepath.Clean(s.Cfg.BaseDir)
	cleanFull := filepath.Clean(fullPath)
	return strings.HasPrefix(cleanFull, base+string(filepath.Separator))
}

// ---- handlers ----

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	b, err := webFS.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "index missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

func (s *Server) handleBriefings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, briefing.ListBriefings(s.Cfg.BaseDir))
}

func (s *Server) handleBriefing(w http.ResponseWriter, r *http.Request) {
	date := r.URL.Query().Get("date")
	slot := r.URL.Query().Get("slot")
	if !dateRe.MatchString(date) || !isValidSlot(slot) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "valid YYYY-MM-DD date and morning|evening slot are required"})
		return
	}
	path := filepath.Join(s.Cfg.BaseDir, date, slot, "playlist.json")
	if !s.isWithinBaseDir(path) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	s.servePlaylist(w, path)
}

func (s *Server) handleLatest(w http.ResponseWriter, r *http.Request) {
	latestPath := filepath.Join(s.Cfg.BaseDir, "latest.json")
	data, err := os.ReadFile(latestPath)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no briefing generated yet"})
		return
	}
	var latest briefing.Latest
	if err := json.Unmarshal(data, &latest); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "latest.json corrupt"})
		return
	}
	if !dateRe.MatchString(latest.Date) || !isValidSlot(latest.Slot) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "latest.json invalid"})
		return
	}
	target := filepath.Join(s.Cfg.BaseDir, latest.Date, latest.Slot, "playlist.json")
	if !s.isWithinBaseDir(target) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	s.servePlaylist(w, target)
}

func (s *Server) servePlaylist(w http.ResponseWriter, path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "playlist not found"})
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write(data)
}

func checkSameOrigin(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			return false
		}
	}
	if sfs := r.Header.Get("Sec-Fetch-Site"); sfs != "" && sfs != "same-origin" && sfs != "none" {
		return false
	}
	if r.Header.Get("X-Requested-With") == "fetch" || r.Header.Get("Sec-Fetch-Site") == "same-origin" {
		return true
	}
	return false
}

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if !checkSameOrigin(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request forbidden"})
		return
	}
	slot := r.URL.Query().Get("slot")
	if slot == "" {
		slot = "morning"
	}
	if !isValidSlot(slot) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slot must be morning or evening"})
		return
	}
	if !s.Gen.TryStart(slot) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "generation already running"})
		return
	}
	baseCtx := s.BaseCtx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	go func() {
		start := time.Now()
		if _, err := s.Gen.GenerateStarted(baseCtx, slot); err != nil {
			log.Printf("[server] generation for %s failed after %s: %v", slot, time.Since(start).Round(time.Second), err)
			return
		}
		log.Printf("[server] generation for %s completed in %s", slot, time.Since(start).Round(time.Second))
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started", "slot": slot})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Gen.Status())
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/audio/")
	full := filepath.Join(s.Cfg.BaseDir, filepath.FromSlash(rel))
	if !s.isWithinBaseDir(full) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	f, err := os.Open(full)
	if err != nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if ct := mime.TypeByExtension(filepath.Ext(full)); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	http.ServeContent(w, r, st.Name(), st.ModTime(), f)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
