package server

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/panos/audio-briefing/internal/briefing"
	"github.com/panos/audio-briefing/internal/config"
)

//go:embed web
var webFS embed.FS

// Server is the HTTP server for the web player and JSON APIs.
type Server struct {
	Cfg *config.Config
	Gen *briefing.Generator

	mu          sync.Mutex
	state       string
	currentSlot string
	lastRun     time.Time
	lastError   string
}

func New(cfg *config.Config, gen *briefing.Generator) *Server {
	return &Server{Cfg: cfg, Gen: gen, state: "idle"}
}

// ListenAndServe builds the mux and serves on addr.
func (s *Server) ListenAndServe(addr string) error {
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
		return err
	}
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))

	return http.ListenAndServe(addr, s.auth(mux))
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.AuthUsername != "" && s.Cfg.AuthPassword != "" {
			u, p, ok := r.BasicAuth()
			if !ok || u != s.Cfg.AuthUsername || p != s.Cfg.AuthPassword {
				w.Header().Set("WWW-Authenticate", `Basic realm="briefing"`)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
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
	if date == "" || slot == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "date and slot are required"})
		return
	}
	path := filepath.Join(s.Cfg.BaseDir, date, slot, "playlist.json")
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
	s.servePlaylist(w, filepath.Join(s.Cfg.BaseDir, latest.Path, "playlist.json"))
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

func (s *Server) handleGenerate(w http.ResponseWriter, r *http.Request) {
	slot := r.URL.Query().Get("slot")
	if slot == "" {
		slot = "morning"
	}
	if slot != "morning" && slot != "evening" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slot must be morning or evening"})
		return
	}
	if !s.tryStart(slot) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "generation already running"})
		return
	}
	go func() {
		start := time.Now()
		if _, err := s.Gen.Generate(context.Background(), slot); err != nil {
			s.finish(slot, err)
			log.Printf("[server] generation for %s failed after %s: %v", slot, time.Since(start).Round(time.Second), err)
			return
		}
		s.finish(slot, nil)
		log.Printf("[server] generation for %s completed in %s", slot, time.Since(start).Round(time.Second))
	}()
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started", "slot": slot})
}

func (s *Server) tryStart(slot string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == "running" {
		return false
	}
	s.state = "running"
	s.currentSlot = slot
	s.lastError = ""
	return true
}

func (s *Server) finish(slot string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = "idle"
	s.lastRun = time.Now()
	if err != nil {
		s.state = "error"
		s.lastError = err.Error()
	}
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"state":        s.state,
		"current_slot": s.currentSlot,
		"last_run":     s.lastRun,
		"last_error":   s.lastError,
	})
}

func (s *Server) handleAudio(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/audio/")
	full := filepath.Join(s.Cfg.BaseDir, filepath.FromSlash(rel))
	base := filepath.Clean(s.Cfg.BaseDir)
	if !strings.HasPrefix(filepath.Clean(full), base) {
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
