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
	ID       string   `json:"id"`
	Type     string   `json:"type"`
	Title    string   `json:"title"`
	Subtitle string   `json:"subtitle,omitempty"`
	AlbumID  string   `json:"album_id,omitempty"`
	URL      string   `json:"url"`
	Duration float64  `json:"duration"`
	Sources  []Source `json:"sources,omitempty"`
}

// Source is one source article backing a news item.
type Source struct {
	Newspaper string `json:"newspaper"`
	Title     string `json:"title"`
	URL       string `json:"url"`
}

// Playlist is the JSON document served to the web player. `Items` holds the
// default genre variant (backward compatible); `Variants` holds additional
// genre-specific item lists keyed by genre id; `Genres` lists the buttons to
// render in display order.
type Playlist struct {
	Date     string            `json:"date"`
	Slot     string            `json:"slot"`
	Created  string            `json:"created"`
	Default  string            `json:"default,omitempty"`
	Genres   []GenreOption     `json:"genres,omitempty"`
	Items    []Item            `json:"items"`
	Variants map[string][]Item `json:"variants,omitempty"`
}

// GenreOption is one music-category button in the player.
type GenreOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
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
	Cfg      *config.Config
	News     *news.Client
	Subsonic *music.Subsonic
	LastFM   *music.LastFM
	TTS      *tts.Client

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

	// Music is optional: set MUSIC_TRACKS=0 to produce a news-only briefing.
	musicEnabled := g.Cfg.MusicTracks > 0

	// Music: fetch the year's albums once, then curate per genre bucket.
	var albums []music.Album
	if musicEnabled {
		if al, err := g.Subsonic.AlbumsByYear(ctx, releaseYear, 500); err != nil {
			log.Printf("[briefing] music album fetch failed for %s: %v", slot, err)
		} else {
			albums = al
		}
	}

	target := g.Cfg.NewsSegments
	if target <= 0 {
		target = 10
	}
	if musicEnabled && g.Cfg.MusicTracks < target {
		target = g.Cfg.MusicTracks
	}

	if len(newsItems) == 0 {
		retErr = fmt.Errorf("nothing to generate: %d news", len(newsItems))
		g.notifyTelegram(ctx, fmt.Sprintf("⚠️ Audio Briefing (%s %s) failed: %v", date, slot, retErr))
		return Playlist{}, retErr
	}
	if musicEnabled && len(albums) == 0 {
		retErr = fmt.Errorf("music enabled but no albums found for year %d", releaseYear)
		g.notifyTelegram(ctx, fmt.Sprintf("⚠️ Audio Briefing (%s %s) failed: %v", date, slot, retErr))
		return Playlist{}, retErr
	}

	// Curate music per bucket (metadata only, before any TTS/download).
	type variant struct {
		id     string
		label  string
		tracks []music.Track
	}
	var variants []variant
	if musicEnabled {
		excludeAlbums := recentAlbumIDs(g.Cfg.BaseDir, 4)
		buckets := music.GroupAlbumsByGenre(albums)
		variants = append(variants, variant{id: "mix", label: "Mix", tracks: g.selectVariantTracks(ctx, albums, target, excludeAlbums)})
		for _, gb := range music.DefaultGenres {
			if gb.ID == "mix" {
				continue
			}
			pool := buckets[gb.ID]
			if len(pool) == 0 {
				continue
			}
			// Skip a redundant "other" button when every album is untagged (it would
			// just duplicate the mix).
			if gb.ID == "other" && len(pool) == len(albums) {
				continue
			}
			variants = append(variants, variant{id: gb.ID, label: gb.Label, tracks: g.selectVariantTracks(ctx, pool, target, excludeAlbums)})
		}
		if len(variants) == 0 || len(variants[0].tracks) == 0 {
			retErr = fmt.Errorf("music selection produced no tracks")
			g.notifyTelegram(ctx, fmt.Sprintf("⚠️ Audio Briefing (%s %s) failed: %v", date, slot, retErr))
			return Playlist{}, retErr
		}
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
		lastTTSCall time.Duration
		hadTTSCall  bool
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

	// Phase 1: synthesize the shared news clips once.
	newsClips, err := g.buildNewsClips(ctx, tmpDir, date, slot, nonSportsNews, sportsNews, target, pauseBeforeTTS, &hadTTSCall, &lastTTSCall)
	if err != nil {
		retErr = err
		return Playlist{}, retErr
	}
	if len(newsClips) == 0 {
		retErr = fmt.Errorf("all news segments failed")
		g.notifyTelegram(ctx, fmt.Sprintf("⚠️ Audio Briefing (%s %s) failed: %v", date, slot, retErr))
		return Playlist{}, retErr
	}

	// Phase 2: assemble the playlist. With music, each variant interleaves
	// news -> intro -> music; without music, the playlist is just the news clips.
	playlist := Playlist{
		Date:     date,
		Slot:     slot,
		Created:  now.Format(time.RFC3339),
		Variants: map[string][]Item{},
	}
	if musicEnabled {
		playlist.Default = "mix"
		for _, v := range variants {
			if len(v.tracks) == 0 {
				continue
			}
			items, err := g.assembleVariant(ctx, tmpDir, date, slot, v.id, newsClips, v.tracks, pauseBeforeTTS, &hadTTSCall, &lastTTSCall)
			if err != nil {
				retErr = err
				return Playlist{}, retErr
			}
			if len(items) == 0 {
				continue
			}
			playlist.Genres = append(playlist.Genres, GenreOption{ID: v.id, Label: v.label})
			if v.id == "mix" {
				playlist.Items = items
			} else {
				playlist.Variants[v.id] = items
			}
		}
	} else {
		playlist.Items = newsClips
	}

	if len(playlist.Items) == 0 {
		retErr = fmt.Errorf("all segments failed")
		g.notifyTelegram(ctx, fmt.Sprintf("⚠️ Audio Briefing (%s %s) failed: %v", date, slot, retErr))
		return Playlist{}, retErr
	}

	if err := ctx.Err(); err != nil {
		retErr = err
		return Playlist{}, retErr
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

	triplets := 0
	for _, it := range playlist.Items {
		if it.Type == "news" {
			triplets++
		}
	}
	if triplets < target {
		g.notifyTelegram(ctx, fmt.Sprintf("⚠️ Audio Briefing (%s %s) published degraded: %d/%d triplets", date, slot, triplets, target))
	}

	log.Printf("[briefing] generated %s: %d variants, %d items (%d triplets) -> %s", slot, len(playlist.Genres), len(playlist.Items), triplets, finalDir)
	return playlist, nil
}

// selectVariantTracks selects a fallback pool (2x target, min 20, capped to the
// bucket size) of tracks for one genre bucket. Metadata-only; audio is
// downloaded later during assembly.
func (g *Generator) selectVariantTracks(ctx context.Context, pool []music.Album, target int, excludeAlbums map[string]bool) []music.Track {
	desired := target * 2
	if desired < 20 {
		desired = 20
	}
	if len(pool) < desired {
		desired = len(pool)
	}
	ts, err := music.SelectTracksFromAlbums(ctx, g.Subsonic, g.LastFM, pool, desired, excludeAlbums)
	if err != nil {
		log.Printf("[briefing] music selection failed: %v", err)
		return nil
	}
	return ts
}

// buildNewsClips synthesizes up to `target` shared news clips (non-sports first,
// then up to 2 sports at the tail). Returns the assembled playlist items.
func (g *Generator) buildNewsClips(ctx context.Context, tmpDir, date, slot string, nonSports, sports []news.Item, target int, pause func() error, hadTTSCall *bool, lastTTSCall *time.Duration) ([]Item, error) {
	nonSportsTarget := target - len(sports)
	if nonSportsTarget < 0 {
		nonSportsTarget = 0
	}
	var items []Item
	nonSportsIdx, sportsIdx, segIdx := 0, 0, 0

	choose := func() (news.Item, bool) {
		if segIdx < nonSportsTarget {
			for nonSportsIdx < len(nonSports) {
				ni := nonSports[nonSportsIdx]
				nonSportsIdx++
				return ni, true
			}
		}
		for sportsIdx < len(sports) {
			ni := sports[sportsIdx]
			sportsIdx++
			return ni, true
		}
		return news.Item{}, false
	}

	for segIdx < target {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		ni, ok := choose()
		if !ok {
			break
		}
		if err := pause(); err != nil {
			return nil, err
		}
		lead := truncateLead(ni.SummaryText, g.Cfg.NewsSpeechMaxChars)
		newsBytes, callDur, err := g.TTS.Synthesize(ctx, lead, g.Cfg.TTSVoiceNews)
		*hadTTSCall = true
		*lastTTSCall = callDur
		if err != nil {
			log.Printf("[briefing] tts news (seg %d) failed: %v", segIdx, err)
			continue
		}
		nf := filepath.Join(tmpDir, fmt.Sprintf("news_%d.mp3", segIdx))
		if !g.speechToFile(ctx, newsBytes, nf, segIdx) {
			continue
		}
		sources := make([]Source, 0, len(ni.Sources))
		for _, s := range ni.Sources {
			sources = append(sources, Source{Newspaper: s.Newspaper, Title: s.Title, URL: s.URL})
		}
		items = append(items, Item{
			ID:       fmt.Sprintf("n%d", segIdx),
			Type:     "news",
			Title:    ni.Topic,
			Subtitle: ni.Category,
			URL:      urlFor(date, slot, fmt.Sprintf("news_%d.mp3", segIdx)),
			Duration: dur(nf),
			Sources:  sources,
		})
		segIdx++
	}
	return items, nil
}

// assembleVariant interleaves the shared news clips with one genre's intro and
// music tracks, writing audio files into tmpDir. It skips tracks that fail to
// download/transcode, stopping the variant early when the pool is exhausted.
func (g *Generator) assembleVariant(ctx context.Context, tmpDir, date, slot, genre string, newsClips []Item, tracks []music.Track, pause func() error, hadTTSCall *bool, lastTTSCall *time.Duration) ([]Item, error) {
	var items []Item
	trackIdx := 0
	for i := 0; i < len(newsClips); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Find the next track that downloads + transcodes successfully.
		var (
			tr music.Track
			mf string
			ok bool
		)
		for trackIdx < len(tracks) {
			cand := tracks[trackIdx]
			trackIdx++
			path := filepath.Join(tmpDir, fmt.Sprintf("music_%s_%d.mp3", genre, i))
			if err := g.fetchAndTranscode(ctx, tmpDir, cand, path, i); err != nil {
				log.Printf("[briefing] music (genre %s, seg %d, song %s) failed: %v", genre, i, cand.SongID, err)
				continue
			}
			tr = cand
			mf = path
			ok = true
			break
		}
		if !ok {
			break
		}

		items = append(items, newsClips[i])

		if g.Cfg.TrackIntros {
			if err := pause(); err != nil {
				return nil, err
			}
			introBytes, callDur, err := g.TTS.Synthesize(ctx, buildIntroText(tr), g.Cfg.TTSVoiceIntro)
			*hadTTSCall = true
			*lastTTSCall = callDur
			if err == nil {
				introFile := filepath.Join(tmpDir, fmt.Sprintf("intro_%s_%d.mp3", genre, i))
				if g.speechToFile(ctx, introBytes, introFile, i) {
					items = append(items, Item{
						ID:       fmt.Sprintf("i%d", i),
						Type:     "intro",
						Title:    tr.Title,
						Subtitle: tr.Artist,
						URL:      urlFor(date, slot, fmt.Sprintf("intro_%s_%d.mp3", genre, i)),
						Duration: dur(introFile),
					})
				}
			} else {
				log.Printf("[briefing] tts intro (genre %s, seg %d) failed: %v (skipping intro)", genre, i, err)
			}
		}

		items = append(items, Item{
			ID:       fmt.Sprintf("m%d", i),
			Type:     "music",
			Title:    tr.Title,
			Subtitle: tr.Artist,
			AlbumID:  tr.AlbumID,
			URL:      urlFor(date, slot, fmt.Sprintf("music_%s_%d.mp3", genre, i)),
			Duration: dur(mf),
		})
	}
	return items, nil
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
	if err := g.Subsonic.DownloadToFile(ctx, tr.SongID, srcPath); err != nil {
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
		for _, it := range allItems(pl) {
			if it.Type == "music" && it.AlbumID != "" {
				out[it.AlbumID] = true
			}
		}
	}
	return out
}

// allItems returns every playable item across the default and genre variants.
func allItems(pl Playlist) []Item {
	items := append([]Item(nil), pl.Items...)
	for _, v := range pl.Variants {
		items = append(items, v...)
	}
	return items
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
