package scheduler

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/panos/audio-briefing/internal/briefing"
	"github.com/panos/audio-briefing/internal/config"
)

var slotNames = []string{"morning", "evening"}

// retryAfter is how long to wait before retrying a slot whose generation failed.
// This mirrors the producer's backoff and keeps a flaky TTS service from causing
// a tight 60-second retry loop.
const retryAfter = 30 * time.Minute

var athens = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		return time.Local
	}
	return loc
}()

// Start runs the generation scheduler: it wakes every minute and generates a
// slot once its time has passed for the day (same-day catch-up) and it has not
// already been generated. A failed run backs off for retryAfter before the next
// attempt. The filesystem is the durable "already done" flag, so restarts are
// safe.
func Start(ctx context.Context, cfg *config.Config, gen *briefing.Generator) {
	go func() {
		var mu sync.Mutex
		nextAttempt := make(map[string]time.Time)

		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				log.Println("[scheduler] stopped")
				return
			case <-ticker.C:
				now := time.Now().In(athens)
				nowMin := now.Hour()*60 + now.Minute()
				for i, slotTime := range cfg.ScheduleSlots {
					name := slotName(i)
					t, err := parseHHMM(slotTime)
					if err != nil {
						log.Printf("[scheduler] invalid slot time %q: %v", slotTime, err)
						continue
					}
					if nowMin < t {
						continue // slot time not reached today
					}
					if alreadyGenerated(cfg.BaseDir, name) {
						continue
					}
					mu.Lock()
					if !nextAttempt[name].IsZero() && now.Before(nextAttempt[name]) {
						mu.Unlock()
						continue // backing off after a recent attempt
					}
					// Optimistic backoff: reserve the next attempt now, so a
					// slow/failed run does not trigger a 60s retry storm.
					nextAttempt[name] = now.Add(retryAfter)
					mu.Unlock()
					log.Printf("[scheduler] triggering %s generation", name)
					go func(slot string) {
						if _, err := gen.Generate(context.Background(), slot); err != nil {
							log.Printf("[scheduler] %s generation failed (retry in ~%s): %v", slot, retryAfter, err)
						}
					}(name)
				}
			}
		}
	}()
}

func slotName(i int) string {
	if i < len(slotNames) {
		return slotNames[i]
	}
	return "slot" + strconv.Itoa(i)
}

func alreadyGenerated(baseDir, slot string) bool {
	date := time.Now().In(athens).Format("2006-01-02")
	_, err := os.Stat(filepath.Join(baseDir, date, slot, "playlist.json"))
	return err == nil
}

func parseHHMM(s string) (int, error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, nil
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, nil
	}
	return h*60 + m, nil
}
