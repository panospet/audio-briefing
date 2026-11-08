package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/panos/audio-briefing/internal/briefing"
	"github.com/panos/audio-briefing/internal/config"
	"github.com/panos/audio-briefing/internal/music"
	"github.com/panos/audio-briefing/internal/news"
	"github.com/panos/audio-briefing/internal/scheduler"
	"github.com/panos/audio-briefing/internal/server"
	"github.com/panos/audio-briefing/internal/tts"
)

func main() {
	cfg := config.Load()
	if cfg.AuthUsername == "" || cfg.AuthPassword == "" {
		log.Fatal("AUTH_USERNAME and AUTH_PASSWORD environment variables are required")
	}

	briefing.CleanupTmpDirs(cfg.BaseDir)

	gen := &briefing.Generator{
		Cfg:       cfg,
		News:      news.NewClient(cfg.DashboardURL, cfg.AuthUsername, cfg.AuthPassword),
		Navidrome: music.NewNavidrome(cfg.NavidromeURL, cfg.NavidromeUser, cfg.NavidromePass),
		LastFM:    music.NewLastFM(cfg.LastFMAPIKey),
		TTS:       tts.NewClient(cfg.VoiceServiceURL, cfg.AuthUsername, cfg.AuthPassword),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := server.New(cfg, gen)
	srv.BaseCtx = ctx

	scheduler.Start(ctx, cfg, gen)

	addr := fmt.Sprintf("%s:%d", cfg.Bind, cfg.Port)
	go func() {
		log.Printf("audio-briefing listening on http://%s", addr)
		if err := srv.ListenAndServe(addr); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}
	gen.Wait()
	log.Println("stopped")
}
