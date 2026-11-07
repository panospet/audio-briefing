package briefing

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/panos/audio-briefing/internal/audio"
	"github.com/panos/audio-briefing/internal/config"
	"github.com/panos/audio-briefing/internal/music"
	"github.com/panos/audio-briefing/internal/news"
	"github.com/panos/audio-briefing/internal/tts"
)

// Item is one playable entry in a playlist.
type Item struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	Title    string  `json:"title"`
	Subtitle string  `json:"subtitle,omitempty"`
	URL      string  `json:"url"`
	Duration float64 `json:"duration"`
}

// Playlist is the JSON document served to the web player.
type Playlist struct {
	Slot    string `json:"slot"`
	Created string `json:"created"`
	Items   []Item `json:"items"`
}

// Latest is the latest.json pointer.
type Latest struct {
	Date string `json:"date"`
	Slot string `json:"slot"`
	Path string `json:"path"`
}

// Generator runs the full pipeline: news + music -> speech + audio -> playlist.
type Generator struct {
	Cfg       *config.Config
	News      *news.Client
	Navidrome *music.Navidrome
	LastFM    *music.LastFM
	TTS       *tts.Client

	mu sync.Mutex
}

const releaseYear = 2026

var athens = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		return time.Local
	}
	return loc
}()

// Generate builds a briefing for the given slot and writes it to disk atomically.
func (g *Generator) Generate(ctx context.Context, slot string) (Playlist, error) {
	if !g.mu.TryLock() {
		return Playlist{}, fmt.Errorf("generation already in progress")
	}
	defer g.mu.Unlock()

	now := time.Now().In(athens)
	date := now.Format("2006-01-02")

	// News (best effort — a failure yields a music-only briefing).
	var newsItems []news.Item
	if ns, err := g.News.Fetch(ctx, slot); err != nil {
		log.Printf("[briefing] news fetch failed for %s: %v (music-only)", slot, err)
	} else {
		newsItems = ns
	}
	if len(newsItems) > g.Cfg.NewsSegments {
		newsItems = newsItems[:g.Cfg.NewsSegments]
	}

	// Music (best effort).
	var tracks []music.Track
	if ts, err := music.SelectTracks(ctx, g.Navidrome, g.LastFM, releaseYear, g.Cfg.MusicTracks); err != nil {
		log.Printf("[briefing] music selection failed for %s: %v", slot, err)
	} else {
		tracks = ts
	}

	n := len(newsItems)
	if len(tracks) < n {
		n = len(tracks)
	}
	if n == 0 {
		return Playlist{}, fmt.Errorf("nothing to generate: %d news, %d tracks", len(newsItems), len(tracks))
	}

	finalDir := filepath.Join(g.Cfg.BaseDir, date, slot)
	tmpDir := finalDir + ".tmp" + fmt.Sprint(now.UnixNano())
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return Playlist{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	var items []Item
	for i := 0; i < n; i++ {
		// Pause between segments so the ~20 TTS requests don't fire back-to-back
		// and trip Microsoft's rate limit (which lets ~8 burst through).
		if i > 0 {
			time.Sleep(time.Duration(g.Cfg.TTSPauseSeconds) * time.Second)
		}
		triplet, ok := g.buildTriplet(ctx, tmpDir, date, slot, i, newsItems[i], tracks[i])
		if !ok {
			log.Printf("[briefing] skipping segment %d (a step failed)", i)
			continue
		}
		items = append(items, triplet...)
	}
	if len(items) == 0 {
		return Playlist{}, fmt.Errorf("all %d segments failed", n)
	}

	playlist := Playlist{Slot: slot, Created: now.Format(time.RFC3339), Items: items}
	if err := writeJSON(filepath.Join(tmpDir, "playlist.json"), playlist); err != nil {
		return Playlist{}, err
	}

	// Atomic publish: remove any previous same-slot dir, then rename tmp -> final.
	_ = os.RemoveAll(finalDir)
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return Playlist{}, err
	}
	committed = true

	writeLatest(g.Cfg.BaseDir, date, slot)
	g.cleanup()

	log.Printf("[briefing] generated %s: %d items -> %s", slot, len(items), finalDir)
	return playlist, nil
}

// buildTriplet builds news -> intro -> music for one index. Returns false (skip
// the whole triplet) if the news or music step fails, keeping alternation clean.
func (g *Generator) buildTriplet(ctx context.Context, dir, date, slot string, i int, ni news.Item, tr music.Track) ([]Item, bool) {
	var items []Item

	// News (speech + loudnorm). Speak a short lead, not the full summary.
	newsBytes, err := g.TTS.Synthesize(ctx, truncateLead(ni.SummaryText, g.Cfg.NewsSpeechMaxChars), g.Cfg.TTSVoiceNews)
	if err != nil {
		log.Printf("[briefing] tts news %d failed: %v", i, err)
		return nil, false
	}
	newsFile := filepath.Join(dir, fmt.Sprintf("news_%d.mp3", i))
	if !g.speechToFile(ctx, newsBytes, newsFile, i) {
		return nil, false
	}
	items = append(items, Item{
		ID: fmt.Sprintf("n%d", i), Type: "news", Title: ni.Topic,
		URL: urlFor(date, slot, fmt.Sprintf("news_%d.mp3", i)), Duration: dur(newsFile),
	})

	// Track intro (optional).
	if g.Cfg.TrackIntros {
		if introBytes, err := g.TTS.Synthesize(ctx, buildIntroText(tr), g.Cfg.TTSVoiceIntro); err == nil {
			introFile := filepath.Join(dir, fmt.Sprintf("intro_%d.mp3", i))
			if g.speechToFile(ctx, introBytes, introFile, i) {
				items = append(items, Item{
					ID: fmt.Sprintf("i%d", i), Type: "intro", Title: tr.Title, Subtitle: tr.Artist,
					URL: urlFor(date, slot, fmt.Sprintf("intro_%d.mp3", i)), Duration: dur(introFile),
				})
			}
		} else {
			log.Printf("[briefing] tts intro %d failed: %v (skipping intro)", i, err)
		}
	}

	// Music (transcode-only, no loudnorm).
	musicFile := filepath.Join(dir, fmt.Sprintf("music_%d.mp3", i))
	if err := g.fetchAndTranscode(ctx, tr, musicFile); err != nil {
		log.Printf("[briefing] music %d failed: %v", i, err)
		return nil, false
	}
	items = append(items, Item{
		ID: fmt.Sprintf("m%d", i), Type: "music", Title: tr.Title, Subtitle: tr.Artist,
		URL: urlFor(date, slot, fmt.Sprintf("music_%d.mp3", i)), Duration: dur(musicFile),
	})

	return items, true
}

// speechToFile writes TTS bytes and normalizes them to MP3 192k. If loudnorm
// fails, the raw bytes are kept so the segment still publishes.
func (g *Generator) speechToFile(ctx context.Context, raw []byte, dst string, i int) bool {
	tmpRaw := dst + ".raw.mp3"
	if err := os.WriteFile(tmpRaw, raw, 0o644); err != nil {
		log.Printf("[briefing] write raw %d failed: %v", i, err)
		return false
	}
	defer os.Remove(tmpRaw)
	if err := audio.NormalizeMP3(ctx, tmpRaw, dst); err != nil {
		log.Printf("[briefing] loudnorm %d failed: %v (keeping raw)", i, err)
		if err := os.Rename(tmpRaw, dst); err != nil {
			return false
		}
	}
	return true
}

// fetchAndTranscode downloads a song's audio and transcodes it to MP3 192k.
func (g *Generator) fetchAndTranscode(ctx context.Context, tr music.Track, dst string) error {
	raw, err := g.Navidrome.Download(ctx, tr.SongID)
	if err != nil {
		return err
	}
	suffix := tr.Suffix
	if suffix == "" {
		suffix = "mp3"
	}
	src, err := os.CreateTemp("", "briefing-src-*."+suffix)
	if err != nil {
		return err
	}
	srcPath := src.Name()
	defer os.Remove(srcPath)
	if _, err := src.Write(raw); err != nil {
		src.Close()
		return err
	}
	src.Close()
	return audio.TranscodeMP3(ctx, srcPath, dst)
}

func buildIntroText(tr music.Track) string {
	parts := []string{fmt.Sprintf("Επόμενο κομμάτι: %s από %s.", tr.Title, tr.Artist)}
	if tr.Album != "" {
		s := fmt.Sprintf("Από το άλμπουμ %s", tr.Album)
		if tr.Year > 0 {
			s += fmt.Sprintf(" του %d", tr.Year)
		}
		if tr.Genre != "" {
			s += fmt.Sprintf(", %s", tr.Genre)
		}
		parts = append(parts, s+".")
	}
	return strings.Join(parts, " ")
}

func urlFor(date, slot, file string) string {
	return fmt.Sprintf("/audio/%s/%s/%s", date, slot, file)
}

// truncateLead shortens text to at most maxChars runes, cutting at the last
// sentence boundary so we never speak a sentence fragment.
func truncateLead(text string, maxChars int) string {
	runes := []rune(text)
	if maxChars <= 0 || len(runes) <= maxChars {
		return text
	}
	cut := maxChars
	for i := maxChars - 1; i >= maxChars/2; i-- {
		switch runes[i] {
		case '.', '!', '?', ';', '…':
			cut = i + 1
			goto done
		}
	}
done:
	return strings.TrimSpace(string(runes[:cut]))
}

func dur(path string) float64 {
	d, err := audio.ProbeDuration(path)
	if err != nil {
		return 0
	}
	return d
}

func writeJSON(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func writeLatest(baseDir, date, slot string) {
	_ = writeJSON(filepath.Join(baseDir, "latest.json"), Latest{Date: date, Slot: slot, Path: filepath.Join(date, slot)})
}

// cleanup removes briefing folders older than the retention window.
func (g *Generator) cleanup() {
	entries, err := os.ReadDir(g.Cfg.BaseDir)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -g.Cfg.RetentionDays)
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) != 10 {
			continue
		}
		d, err := time.Parse("2006-01-02", e.Name())
		if err != nil {
			continue
		}
		if d.Before(cutoff) {
			p := filepath.Join(g.Cfg.BaseDir, e.Name())
			if err := os.RemoveAll(p); err == nil {
				log.Printf("[briefing] retention: removed %s", e.Name())
			}
		}
	}
}

// ListBriefings scans the base dir and returns available {date, slot} pairs.
func ListBriefings(baseDir string) []Latest {
	var out []Latest
	dates, err := os.ReadDir(baseDir)
	if err != nil {
		return nil
	}
	for _, de := range dates {
		if !de.IsDir() || len(de.Name()) != 10 {
			continue
		}
		slots, err := os.ReadDir(filepath.Join(baseDir, de.Name()))
		if err != nil {
			continue
		}
		for _, se := range slots {
			if !se.IsDir() {
				continue
			}
			if _, err := os.Stat(filepath.Join(baseDir, de.Name(), se.Name(), "playlist.json")); err == nil {
				out = append(out, Latest{Date: de.Name(), Slot: se.Name(), Path: filepath.Join(de.Name(), se.Name())})
			}
		}
	}
	return out
}
