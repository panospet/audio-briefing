package briefing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

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
	AlbumID  string  `json:"album_id,omitempty"`
	URL      string  `json:"url"`
	Duration float64 `json:"duration"`
}

// Playlist is the JSON document served to the web player.
type Playlist struct {
	Date    string `json:"date"`
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

// StatusSnapshot represents the current generator state for /api/status.
type StatusSnapshot struct {
	State       string    `json:"state"`
	CurrentSlot string    `json:"current_slot"`
	LastRun     time.Time `json:"last_run"`
	LastError   string    `json:"last_error"`
}

// Generator runs the full pipeline: news + music -> speech + audio -> playlist.
type Generator struct {
	Cfg       *config.Config
	News      *news.Client
	Navidrome *music.Navidrome
	LastFM    *music.LastFM
	TTS       *tts.Client

	mu          sync.Mutex
	statusMu    sync.Mutex
	state       string
	currentSlot string
	lastRun     time.Time
	lastError   string
	wg          sync.WaitGroup
}

const releaseYear = 2026

// Athens is the Europe/Athens timezone used across the service.
var Athens = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		return time.Local
	}
	return loc
}()

// TryStart marks the generator as running for slot if idle.
func (g *Generator) TryStart(slot string) bool {
	g.statusMu.Lock()
	defer g.statusMu.Unlock()
	if g.state == "running" {
		return false
	}
	g.state = "running"
	g.currentSlot = slot
	g.lastError = ""
	g.wg.Add(1)
	return true
}

func (g *Generator) finish(slot string, err error) {
	g.statusMu.Lock()
	defer g.statusMu.Unlock()
	g.state = "idle"
	g.lastRun = time.Now()
	if err != nil {
		g.state = "error"
		g.lastError = err.Error()
	}
	g.wg.Done()
}

// Status returns a snapshot of the current generation status.
func (g *Generator) Status() StatusSnapshot {
	g.statusMu.Lock()
	defer g.statusMu.Unlock()
	st := g.state
	if st == "" {
		st = "idle"
	}
	return StatusSnapshot{
		State:       st,
		CurrentSlot: g.currentSlot,
		LastRun:     g.lastRun,
		LastError:   g.lastError,
	}
}

// Wait blocks until any in-flight generation completes.
func (g *Generator) Wait() {
	g.wg.Wait()
}

// Generate acquires the generation state lock and builds a briefing for the given slot.
func (g *Generator) Generate(ctx context.Context, slot string) (Playlist, error) {
	if !g.TryStart(slot) {
		return Playlist{}, fmt.Errorf("generation already in progress")
	}
	return g.GenerateStarted(ctx, slot)
}

// GenerateStarted builds a briefing assuming TryStart(slot) has already succeeded.
func (g *Generator) GenerateStarted(ctx context.Context, slot string) (pl Playlist, retErr error) {
	defer func() {
		g.finish(slot, retErr)
	}()

	g.mu.Lock()
	defer g.mu.Unlock()

	now := time.Now().In(Athens)
	date := now.Format("2006-01-02")

	// News (best effort — fetch & diversify).
	var newsItems []news.Item
	if ns, err := g.News.Fetch(ctx, slot); err != nil {
		log.Printf("[briefing] news fetch failed for %s: %v", slot, err)
	} else {
		newsItems = selectDiverseNews(ns)
	}

	// Music (request 2x pool for fallback + exclude recent albums).
	var tracks []music.Track
	excludeAlbums := recentAlbumIDs(g.Cfg.BaseDir, 4)
	desiredTracks := g.Cfg.MusicTracks * 2
	if desiredTracks < 20 {
		desiredTracks = 20
	}
	if ts, err := music.SelectTracks(ctx, g.Navidrome, g.LastFM, releaseYear, desiredTracks, excludeAlbums); err != nil {
		log.Printf("[briefing] music selection failed for %s: %v", slot, err)
	} else {
		tracks = ts
	}

	target := g.Cfg.NewsSegments
	if target <= 0 {
		target = 10
	}
	if g.Cfg.MusicTracks > 0 && g.Cfg.MusicTracks < target {
		target = g.Cfg.MusicTracks
	}
	if len(newsItems) == 0 || len(tracks) == 0 {
		retErr = fmt.Errorf("nothing to generate: %d news, %d tracks", len(newsItems), len(tracks))
		g.notifyTelegram(ctx, fmt.Sprintf("⚠️ Audio Briefing (%s %s) failed: %v", date, slot, retErr))
		return Playlist{}, retErr
	}

	finalDir := filepath.Join(g.Cfg.BaseDir, date, slot)
	tmpDir := finalDir + ".tmp" + fmt.Sprint(now.UnixNano())
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		retErr = err
		return Playlist{}, retErr
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(tmpDir)
		}
	}()

	// Partition news into non-sports and sports (max 2) so sports are always
	// placed at the very end of the briefing while preserving non-sports fallback headroom.
	var nonSportsNews, sportsNews []news.Item
	for _, ni := range newsItems {
		if strings.EqualFold(strings.TrimSpace(ni.Category), "Αθλητισμός") {
			if len(sportsNews) < 2 {
				sportsNews = append(sportsNews, ni)
			}
		} else {
			nonSportsNews = append(nonSportsNews, ni)
		}
	}

	var (
		items        []Item
		nonSportsIdx int
		sportsIdx    int
		trackIdx     int
		segIdx       int
		lastTTSCall  time.Duration
		hadTTSCall   bool
	)

	pauseBeforeTTS := func() error {
		if !hadTTSCall || lastTTSCall < 500*time.Millisecond || g.Cfg.TTSPauseSeconds <= 0 {
			return nil
		}
		timer := time.NewTimer(time.Duration(g.Cfg.TTSPauseSeconds) * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}

	nonSportsTarget := target - len(sportsNews)
	if nonSportsTarget < 0 {
		nonSportsTarget = 0
	}

	for segIdx < target && (nonSportsIdx < len(nonSportsNews) || sportsIdx < len(sportsNews)) && trackIdx < len(tracks) {
		if err := ctx.Err(); err != nil {
			retErr = err
			return Playlist{}, retErr
		}

		// Step 1: Acquire a working news clip (non-sports first, then sports for the final slots).
		var (
			chosenNews news.Item
			newsFile   string
			newsOK     bool
		)
		trySynthesizeNews := func(ni news.Item) (string, bool, error) {
			if err := pauseBeforeTTS(); err != nil {
				return "", false, err
			}
			lead := truncateLead(ni.SummaryText, g.Cfg.NewsSpeechMaxChars)
			newsBytes, callDur, err := g.TTS.Synthesize(ctx, lead, g.Cfg.TTSVoiceNews)
			hadTTSCall = true
			lastTTSCall = callDur
			if err != nil {
				log.Printf("[briefing] tts news (seg %d) failed: %v", segIdx, err)
				return "", false, nil
			}
			nf := filepath.Join(tmpDir, fmt.Sprintf("news_%d.mp3", segIdx))
			if !g.speechToFile(ctx, newsBytes, nf, segIdx) {
				return "", false, nil
			}
			return nf, true, nil
		}

		if segIdx < nonSportsTarget {
			for nonSportsIdx < len(nonSportsNews) {
				ni := nonSportsNews[nonSportsIdx]
				nonSportsIdx++
				nf, ok, err := trySynthesizeNews(ni)
				if err != nil {
					retErr = err
					return Playlist{}, retErr
				}
				if ok {
					chosenNews = ni
					newsFile = nf
					newsOK = true
					break
				}
			}
		}
		if !newsOK {
			for sportsIdx < len(sportsNews) {
				ni := sportsNews[sportsIdx]
				sportsIdx++
				nf, ok, err := trySynthesizeNews(ni)
				if err != nil {
					retErr = err
					return Playlist{}, retErr
				}
				if ok {
					chosenNews = ni
					newsFile = nf
					newsOK = true
					break
				}
			}
		}
		if !newsOK {
			break
		}

		// Step 2: Acquire a working music track from the track queue (without discarding the news clip).
		var (
			chosenTrack music.Track
			musicFile   string
			musicOK     bool
		)
		for trackIdx < len(tracks) {
			tr := tracks[trackIdx]
			trackIdx++
			mf := filepath.Join(tmpDir, fmt.Sprintf("music_%d.mp3", segIdx))
			if err := g.fetchAndTranscode(ctx, tmpDir, tr, mf, segIdx); err != nil {
				log.Printf("[briefing] music (seg %d, song %s) failed: %v", segIdx, tr.SongID, err)
				continue
			}
			chosenTrack = tr
			musicFile = mf
			musicOK = true
			break
		}
		if !musicOK {
			_ = os.Remove(newsFile)
			break
		}

		// Append news item.
		items = append(items, Item{
			ID:       fmt.Sprintf("n%d", segIdx),
			Type:     "news",
			Title:    chosenNews.Topic,
			Subtitle: chosenNews.Category,
			URL:      urlFor(date, slot, fmt.Sprintf("news_%d.mp3", segIdx)),
			Duration: dur(newsFile),
		})

		// Step 3: Optional track intro for the chosen track.
		if g.Cfg.TrackIntros {
			if err := pauseBeforeTTS(); err != nil {
				retErr = err
				return Playlist{}, retErr
			}
			introBytes, callDur, err := g.TTS.Synthesize(ctx, buildIntroText(chosenTrack), g.Cfg.TTSVoiceIntro)
			hadTTSCall = true
			lastTTSCall = callDur
			if err == nil {
				introFile := filepath.Join(tmpDir, fmt.Sprintf("intro_%d.mp3", segIdx))
				if g.speechToFile(ctx, introBytes, introFile, segIdx) {
					items = append(items, Item{
						ID:       fmt.Sprintf("i%d", segIdx),
						Type:     "intro",
						Title:    chosenTrack.Title,
						Subtitle: chosenTrack.Artist,
						URL:      urlFor(date, slot, fmt.Sprintf("intro_%d.mp3", segIdx)),
						Duration: dur(introFile),
					})
				}
			} else {
				log.Printf("[briefing] tts intro %d failed: %v (skipping intro)", segIdx, err)
			}
		}

		// Append music item.
		items = append(items, Item{
			ID:       fmt.Sprintf("m%d", segIdx),
			Type:     "music",
			Title:    chosenTrack.Title,
			Subtitle: chosenTrack.Artist,
			AlbumID:  chosenTrack.AlbumID,
			URL:      urlFor(date, slot, fmt.Sprintf("music_%d.mp3", segIdx)),
			Duration: dur(musicFile),
		})

		segIdx++
	}

	if len(items) == 0 {
		retErr = fmt.Errorf("all segments failed")
		g.notifyTelegram(ctx, fmt.Sprintf("⚠️ Audio Briefing (%s %s) failed: %v", date, slot, retErr))
		return Playlist{}, retErr
	}

	if err := ctx.Err(); err != nil {
		retErr = err
		return Playlist{}, retErr
	}

	playlist := Playlist{
		Date:    date,
		Slot:    slot,
		Created: now.Format(time.RFC3339),
		Items:   items,
	}
	if err := writeJSON(filepath.Join(tmpDir, "playlist.json"), playlist); err != nil {
		retErr = err
		return Playlist{}, retErr
	}

	// Atomic publish: remove any previous same-slot dir, then rename tmp -> final.
	_ = os.RemoveAll(finalDir)
	if err := os.Rename(tmpDir, finalDir); err != nil {
		retErr = err
		return Playlist{}, retErr
	}
	committed = true

	writeLatest(g.Cfg.BaseDir, date, slot)
	g.cleanup(date)

	if segIdx < target {
		g.notifyTelegram(ctx, fmt.Sprintf("⚠️ Audio Briefing (%s %s) published degraded: %d/%d triplets", date, slot, segIdx, target))
	}

	log.Printf("[briefing] generated %s: %d items (%d triplets) -> %s", slot, len(items), segIdx, finalDir)
	return playlist, nil
}

// speechToFile writes TTS bytes and normalizes them to MP3 192k. If loudnorm
// fails, the raw bytes are kept only if context is still active and ffprobe validates them.
func (g *Generator) speechToFile(ctx context.Context, raw []byte, dst string, i int) bool {
	tmpRaw := dst + ".raw.mp3"
	if err := os.WriteFile(tmpRaw, raw, 0o644); err != nil {
		log.Printf("[briefing] write raw %d failed: %v", i, err)
		return false
	}
	defer os.Remove(tmpRaw)
	if err := audio.NormalizeMP3(ctx, tmpRaw, dst); err != nil {
		if ctx.Err() != nil {
			return false
		}
		d, probeErr := audio.ProbeDuration(tmpRaw)
		if probeErr != nil || d <= 0 {
			log.Printf("[briefing] loudnorm %d failed (%v) and raw probe invalid (%v)", i, err, probeErr)
			return false
		}
		log.Printf("[briefing] loudnorm %d failed: %v (keeping verified raw, %.1fs)", i, err, d)
		if err := os.Rename(tmpRaw, dst); err != nil {
			return false
		}
	}
	return true
}

// fetchAndTranscode streams a song's audio to tmpDir and transcodes it to MP3 192k.
func (g *Generator) fetchAndTranscode(ctx context.Context, tmpDir string, tr music.Track, dst string, segIdx int) error {
	suffix := tr.Suffix
	if suffix == "" {
		suffix = "mp3"
	}
	srcPath := filepath.Join(tmpDir, fmt.Sprintf("src_%d.%s", segIdx, suffix))
	defer os.Remove(srcPath)
	if err := g.Navidrome.DownloadToFile(ctx, tr.SongID, srcPath); err != nil {
		return err
	}
	return audio.TranscodeMP3(ctx, srcPath, dst)
}

func buildIntroText(tr music.Track) string {
	parts := []string{fmt.Sprintf("Επόμενο κομμάτι: %s από %s.", tr.Title, tr.Artist)}
	album := strings.TrimSpace(tr.Album)
	title := strings.TrimSpace(tr.Title)
	genre := strings.TrimSpace(tr.Genre)
	if album != "" && !strings.EqualFold(album, title) {
		s := fmt.Sprintf("Από το άλμπουμ %s", album)
		if genre != "" {
			s += fmt.Sprintf(", %s", genre)
		}
		parts = append(parts, s+".")
	} else if genre != "" {
		parts = append(parts, fmt.Sprintf("Είδος: %s.", genre))
	}
	return strings.Join(parts, " ")
}

func urlFor(date, slot, file string) string {
	return fmt.Sprintf("/audio/%s/%s/%s", date, slot, file)
}

var greekAbbrevs = []string{
	"π.χ.", "κ.", "χλμ.", "κ.ά.", "κ.λπ.", "κλπ.", "δηλ.", "σελ.", "αρ.", "βλ.", "π.μ.", "μ.μ.", "α.ε.", "ο.ε.",
}

func isSentenceEnd(runes []rune, i int) bool {
	r := runes[i]
	if r != '.' && r != '!' && r != '?' && r != ';' && r != '…' {
		return false
	}
	// Must be at end of slice or followed by whitespace or closing quote.
	if i+1 < len(runes) {
		next := runes[i+1]
		if !unicode.IsSpace(next) && next != '»' && next != '"' && next != '\'' && next != ')' {
			return false
		}
	}
	if r == '.' {
		// Check digit.digit (e.g. 60.000)
		if i > 0 && i+1 < len(runes) && unicode.IsDigit(runes[i-1]) && unicode.IsDigit(runes[i+1]) {
			return false
		}
		// Check single capital letter initial before dot (e.g. "Κ. Μητσοτάκης")
		if i > 0 && unicode.IsUpper(runes[i-1]) && (i == 1 || unicode.IsSpace(runes[i-2])) {
			return false
		}
		// Check common Greek abbreviations ending at i.
		prefix := strings.ToLower(string(runes[:i+1]))
		for _, abbr := range greekAbbrevs {
			if strings.HasSuffix(prefix, abbr) {
				prevIdx := len(prefix) - len(abbr)
				if prevIdx == 0 || !unicode.IsLetter([]rune(prefix)[prevIdx-1]) {
					return false
				}
			}
		}
	}
	return true
}

// truncateLead shortens text to at most maxChars runes, cutting at the last
// sentence boundary or falling back to a word boundary with an ellipsis.
func truncateLead(text string, maxChars int) string {
	text = strings.TrimSpace(text)
	runes := []rune(text)
	if maxChars <= 0 || len(runes) <= maxChars {
		return text
	}
	for i := maxChars - 1; i >= maxChars/2; i-- {
		if isSentenceEnd(runes, i) {
			return strings.TrimSpace(string(runes[:i+1]))
		}
	}
	for i := maxChars - 1; i >= maxChars/2; i-- {
		if unicode.IsSpace(runes[i]) {
			trimmed := strings.TrimRightFunc(string(runes[:i]), func(r rune) bool {
				return unicode.IsSpace(r) || r == ',' || r == ':' || r == '-' || r == '–'
			})
			return trimmed + "…"
		}
	}
	return strings.TrimSpace(string(runes[:maxChars])) + "…"
}

// selectDiverseNews orders news items so no non-"Ελλάδα" category exceeds 2 items
// and near-duplicate titles (sharing >= 2 significant words) are deferred to the
// fallback tail.
func selectDiverseNews(items []news.Item) []news.Item {
	if len(items) <= 1 {
		return items
	}
	var primary, overflow []news.Item
	catCount := make(map[string]int)
	var chosenWordSets []map[string]bool

	for _, it := range items {
		cat := strings.TrimSpace(it.Category)
		if cat != "" && cat != "Ελλάδα" && catCount[cat] >= 2 {
			overflow = append(overflow, it)
			continue
		}
		words := significantWords(it.Topic)
		duplicate := false
		for _, prev := range chosenWordSets {
			shared := 0
			for w := range words {
				if prev[w] {
					shared++
				}
			}
			if shared >= 2 {
				duplicate = true
				break
			}
		}
		if duplicate {
			overflow = append(overflow, it)
			continue
		}
		primary = append(primary, it)
		if cat != "" {
			catCount[cat]++
		}
		if len(words) > 0 {
			chosenWordSets = append(chosenWordSets, words)
		}
	}
	return append(primary, overflow...)
}

func significantWords(title string) map[string]bool {
	out := make(map[string]bool)
	var cur []rune
	flush := func() {
		if len(cur) >= 4 {
			out[strings.ToLower(string(cur))] = true
		}
		cur = cur[:0]
	}
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur = append(cur, r)
		} else {
			flush()
		}
	}
	flush()
	return out
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

func slotRank(slot string) int {
	if slot == "evening" {
		return 2
	}
	return 1
}

// CompareBriefingOrder returns >0 if a is newer than b, <0 if older, 0 if equal.
func CompareBriefingOrder(aDate, aSlot, bDate, bSlot string) int {
	if aDate != bDate {
		return strings.Compare(aDate, bDate)
	}
	return slotRank(aSlot) - slotRank(bSlot)
}

func writeLatest(baseDir, date, slot string) {
	latestPath := filepath.Join(baseDir, "latest.json")
	if data, err := os.ReadFile(latestPath); err == nil {
		var cur Latest
		if json.Unmarshal(data, &cur) == nil && cur.Date != "" {
			if CompareBriefingOrder(date, slot, cur.Date, cur.Slot) < 0 {
				return
			}
		}
	}
	tmpPath := latestPath + ".tmp"
	if err := writeJSON(tmpPath, Latest{Date: date, Slot: slot, Path: filepath.Join(date, slot)}); err == nil {
		_ = os.Rename(tmpPath, latestPath)
	}
}

// CleanupTmpDirs removes orphaned *.tmp* directories and files left behind by interrupted runs.
func CleanupTmpDirs(baseDir string) {
	_ = os.Remove(filepath.Join(baseDir, "latest.json.tmp"))
	dates, err := os.ReadDir(baseDir)
	if err != nil {
		return
	}
	for _, de := range dates {
		if !de.IsDir() {
			continue
		}
		dateDir := filepath.Join(baseDir, de.Name())
		entries, err := os.ReadDir(dateDir)
		if err != nil {
			continue
		}
		for _, se := range entries {
			if strings.Contains(se.Name(), ".tmp") {
				p := filepath.Join(dateDir, se.Name())
				if err := os.RemoveAll(p); err == nil {
					log.Printf("[briefing] removed orphaned temp dir %s/%s", de.Name(), se.Name())
				}
			}
		}
	}
}

// cleanup removes briefing folders older than the retention window.
func (g *Generator) cleanup(currentDate string) {
	if g.Cfg.RetentionDays <= 0 {
		return
	}
	entries, err := os.ReadDir(g.Cfg.BaseDir)
	if err != nil {
		return
	}
	now := time.Now().In(Athens)
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, Athens)
	cutoff := today.AddDate(0, 0, -g.Cfg.RetentionDays)
	for _, e := range entries {
		if !e.IsDir() || len(e.Name()) != 10 || e.Name() == currentDate {
			continue
		}
		d, err := time.ParseInLocation("2006-01-02", e.Name(), Athens)
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

// ListBriefings scans the base dir and returns available {date, slot} pairs sorted newest-first.
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
			if !se.IsDir() || strings.Contains(se.Name(), ".tmp") {
				continue
			}
			if _, err := os.Stat(filepath.Join(baseDir, de.Name(), se.Name(), "playlist.json")); err == nil {
				out = append(out, Latest{Date: de.Name(), Slot: se.Name(), Path: filepath.Join(de.Name(), se.Name())})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return CompareBriefingOrder(out[i].Date, out[i].Slot, out[j].Date, out[j].Slot) > 0
	})
	return out
}

func recentAlbumIDs(baseDir string, limit int) map[string]bool {
	out := make(map[string]bool)
	list := ListBriefings(baseDir)
	if len(list) > limit {
		list = list[:limit]
	}
	for _, b := range list {
		data, err := os.ReadFile(filepath.Join(baseDir, b.Path, "playlist.json"))
		if err != nil {
			continue
		}
		var pl Playlist
		if json.Unmarshal(data, &pl) != nil {
			continue
		}
		for _, it := range pl.Items {
			if it.Type == "music" && it.AlbumID != "" {
				out[it.AlbumID] = true
			}
		}
	}
	return out
}

func (g *Generator) notifyTelegram(ctx context.Context, text string) {
	if g.Cfg == nil || g.Cfg.TelegramToken == "" || g.Cfg.TelegramChatID == "" {
		return
	}
	NotifyTelegram(ctx, g.Cfg.TelegramToken, g.Cfg.TelegramChatID, text)
}

// NotifyTelegram sends a short alert message via the Telegram Bot API.
func NotifyTelegram(ctx context.Context, token, chatID, text string) {
	if token == "" || chatID == "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	body, err := json.Marshal(map[string]string{
		"chat_id": chatID,
		"text":    text,
	})
	if err != nil {
		return
	}
	u := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[briefing] telegram alert failed: %v", err)
		return
	}
	_ = resp.Body.Close()
}
