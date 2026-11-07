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

	gen := &briefing.Generator{
		Cfg:       cfg,
		News:      news.NewClient(cfg.DashboardURL, cfg.AuthUsername, cfg.AuthPassword),
		Navidrome: music.NewNavidrome(cfg.NavidromeURL, cfg.NavidromeUser, cfg.NavidromePass),
		LastFM:    music.NewLastFM(cfg.LastFMAPIKey),
		TTS:       tts.NewClient(cfg.VoiceServiceURL, cfg.AuthUsername, cfg.AuthPassword),
	}

	srv := server.New(cfg, gen)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = shutdownCtx
	log.Println("stopped")
}
