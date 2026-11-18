package music

import (
	"context"
	"crypto/md5"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ---------- Subsonic ----------

// Subsonic is a minimal Subsonic API client for discovery + audio download.
// It speaks the open Subsonic REST protocol, so it works against any
// Subsonic-compatible server (Navidrome, Gonic, Airsonic, etc.).
type Subsonic struct {
	baseURL string
	user    string
	pass    string
	client  *http.Client
}

func NewSubsonic(baseURL, user, pass string) *Subsonic {
	return &Subsonic{baseURL: baseURL, user: user, pass: pass, client: &http.Client{Timeout: 180 * time.Second}}
}

func (n *Subsonic) auth() url.Values {
	salt := make([]byte, 6)
	_, _ = cryptorand.Read(salt)
	saltStr := hex.EncodeToString(salt)[:6]
	hash := md5.Sum([]byte(n.pass + saltStr))
	return url.Values{
		"u": {n.user}, "t": {hex.EncodeToString(hash[:])}, "s": {saltStr},
		"v": {"1.16.1"}, "c": {"audio-briefing"}, "f": {"json"},
	}
}

func (n *Subsonic) buildURL(endpoint string, params url.Values) string {
	q := n.auth()
	for k, vs := range params {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	return fmt.Sprintf("%s/rest/%s?%s", n.baseURL, endpoint, q.Encode())
}

func (n *Subsonic) get(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	u := n.buildURL(endpoint, params)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("subsonic %s: status %d", endpoint, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Genre is a genre tag object as returned by the Subsonic API.
type Genre struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Album is a compact album summary from getAlbumList2.
type Album struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Artist    string  `json:"artist"`
	Year      int     `json:"year"`
	Genre     string  `json:"genre"`
	Genres    []Genre `json:"genres"`
	SongCount int     `json:"songCount"`
}

// Song is a track from getAlbum.
type Song struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Artist     string `json:"artist"`
	Album      string `json:"album"`
	Track      int    `json:"track"`
	DiscNumber int    `json:"discNumber"`
	PlayCount  int    `json:"playCount"`
	Duration   int    `json:"duration"`
	Year       int    `json:"year"`
	Suffix     string `json:"suffix"`
	Path       string `json:"path"`
}

type subsonicError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type subsonic struct {
	Status     string        `json:"status"`
	Error      subsonicError `json:"error"`
	AlbumList2 albumList2    `json:"albumList2"`
	Album      albumDetail   `json:"album"`
}

type albumList2 struct {
	Album []Album `json:"album"`
}

type albumDetail struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Artist string  `json:"artist"`
	Year   int     `json:"year"`
	Genre  string  `json:"genre"`
	Genres []Genre `json:"genres"`
	Song   []Song  `json:"song"`
}

func checkSubsonicStatus(endpoint string, s subsonic) error {
	if s.Status != "" && s.Status != "ok" {
		if s.Error.Message != "" {
			return fmt.Errorf("subsonic %s failed (code %d): %s", endpoint, s.Error.Code, s.Error.Message)
		}
		return fmt.Errorf("subsonic %s status %q", endpoint, s.Status)
	}
	return nil
}

// AlbumsByYear returns all albums released in the given year, paging in batches
// of pageSize until exhausted.
func (n *Subsonic) AlbumsByYear(ctx context.Context, year, pageSize int) ([]Album, error) {
	if pageSize <= 0 || pageSize > 500 {
		pageSize = 500
	}
	var all []Album
	for offset := 0; offset < 10000; offset += pageSize {
		data, err := n.get(ctx, "getAlbumList2", url.Values{
			"type":     {"byYear"},
			"fromYear": {fmt.Sprint(year)},
			"toYear":   {fmt.Sprint(year)},
			"size":     {fmt.Sprint(pageSize)},
			"offset":   {fmt.Sprint(offset)},
		})
		if err != nil {
			return nil, err
		}
		var res struct {
			SubsonicResponse subsonic `json:"subsonic-response"`
		}
		if err := json.Unmarshal(data, &res); err != nil {
			return nil, err
		}
		if err := checkSubsonicStatus("getAlbumList2", res.SubsonicResponse); err != nil {
			return nil, err
		}
		page := res.SubsonicResponse.AlbumList2.Album
		all = append(all, page...)
		if len(page) < pageSize {
			break
		}
	}
	return all, nil
}

// GetAlbum returns the album and its songs.
func (n *Subsonic) GetAlbum(ctx context.Context, id string) (*Album, []Song, error) {
	data, err := n.get(ctx, "getAlbum", url.Values{"id": {id}})
	if err != nil {
		return nil, nil, err
	}
	var res struct {
		SubsonicResponse subsonic `json:"subsonic-response"`
	}
	if err := json.Unmarshal(data, &res); err != nil {
		return nil, nil, err
	}
	if err := checkSubsonicStatus("getAlbum", res.SubsonicResponse); err != nil {
		return nil, nil, err
	}
	a := res.SubsonicResponse.Album
	album := &Album{ID: a.ID, Name: a.Name, Artist: a.Artist, Year: a.Year, Genre: a.Genre, Genres: a.Genres}
	return album, a.Song, nil
}

// DownloadToFile streams the original audio for songID directly to dstPath,
// avoiding buffering full lossless files in RAM or writing to container /tmp.
func (n *Subsonic) DownloadToFile(ctx context.Context, songID, dstPath string) error {
	u := n.buildURL("download", url.Values{"id": {songID}})
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("subsonic download: status %d", resp.StatusCode)
	}
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "application/json") || strings.Contains(ct, "xml") {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("subsonic download returned error payload: %s", string(body))
	}
	f, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	nBytes, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(dstPath)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(dstPath)
		return closeErr
	}
	if nBytes == 0 {
		_ = os.Remove(dstPath)
		return fmt.Errorf("subsonic download returned 0 bytes")
	}
	return nil
}

// ---------- Last.fm ----------

// LastFM is a minimal client for artist top tracks.
type LastFM struct {
	apiKey string
	client *http.Client
}

func NewLastFM(apiKey string) *LastFM {
	return &LastFM{apiKey: apiKey, client: &http.Client{Timeout: 20 * time.Second}}
}

// TopTracks returns the artist's top track names in popularity order.
func (l *LastFM) TopTracks(ctx context.Context, artist string, limit int) ([]string, error) {
	if l.apiKey == "" {
		return nil, fmt.Errorf("no last.fm api key")
	}
	q := url.Values{}
	q.Set("method", "artist.gettoptracks")
	q.Set("artist", artist)
	q.Set("api_key", l.apiKey)
	q.Set("format", "json")
	q.Set("limit", fmt.Sprint(limit))
	u := "https://ws.audioscrobbler.com/2.0/?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := l.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lastfm status %d", resp.StatusCode)
	}
	var res struct {
		TopTracks struct {
			Track []struct {
				Name string `json:"name"`
			} `json:"track"`
		} `json:"toptracks"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(res.TopTracks.Track))
	for _, t := range res.TopTracks.Track {
		names = append(names, t.Name)
	}
	return names, nil
}

// ---------- Genre buckets ----------

// GenreBucket is one curated music category surfaced in the web player.
type GenreBucket struct {
	ID    string
	Label string
}

// DefaultGenres is the curated list of genre buckets in display order. The
// "mix" bucket is special: it selects from every album regardless of genre.
var DefaultGenres = []GenreBucket{
	{ID: "mix", Label: "Mix"},
	{ID: "electronic", Label: "Electronic"},
	{ID: "rock", Label: "Rock"},
	{ID: "pop", Label: "Pop"},
	{ID: "hiphop", Label: "Hip-Hop"},
	{ID: "jazz", Label: "Jazz"},
	{ID: "world", Label: "World"},
	{ID: "classical", Label: "Classical"},
	{ID: "other", Label: "Other"},
}

// genreKeywords maps curated buckets to the messy tags that fall into them.
// Matching is longest-keyword-wins so specific tags ("indie pop", "dance-pop")
// outrank generic ones ("pop", "indie", "dance").
var genreKeywords = []struct {
	id   string
	keys []string
}{
	{"classical", []string{"classical", "orchestral", "symphonic", "symphony", "soundtrack", "score", "chamber", "opera", "neoclassical", "modern classical"}},
	{"jazz", []string{"jazz", "blues", "soul", "funk", "r&b", "rnb", "rhythm and blues", "swing", "bebop", "gospel"}},
	{"hiphop", []string{"hip-hop", "hip hop", "hiphop", "rap", "trap", "grime"}},
	{"electronic", []string{"electronic", "electronica", "electro", "edm", "house", "techno", "trance", "ambient", "dance", "drum and bass", "drum & bass", "dnb", "dubstep", "idm", "synthwave", "disco", "industrial", "breakbeat"}},
	{"rock", []string{"rock", "metal", "punk", "indie", "alternative", "grunge", "post-punk", "hardcore", "emo", "shoegaze", "psychedelic", "post-rock", "folk rock"}},
	{"pop", []string{"pop", "synthpop", "synth-pop", "electropop", "dance-pop", "dance pop", "indie pop", "dream pop", "k-pop", "j-pop"}},
	{"world", []string{"world", "folk", "traditional", "greek", "greece", "laika", "λαϊκ", "entexno", "έντεχνο", "rebetiko", "ρεμπέτικο", "ethnic", "latin", "reggae", "afro", "african", "balkan", "country", "americana", "celtic", "fado", "flamenco"}},
}

// ClassifyGenre maps a raw genre tag to a curated bucket id, defaulting to
// "other" when nothing matches.
func ClassifyGenre(raw string) string {
	g := strings.ToLower(strings.TrimSpace(raw))
	if g == "" {
		return "other"
	}
	best := ""
	bestLen := 0
	for _, grp := range genreKeywords {
		for _, k := range grp.keys {
			if strings.Contains(g, k) && len(k) > bestLen {
				best = grp.id
				bestLen = len(k)
			}
		}
	}
	if best == "" {
		return "other"
	}
	return best
}

// albumGenre resolves an album's primary genre tag.
func albumGenre(al Album) string {
	if len(al.Genres) > 0 {
		if al.Genres[0].Name != "" {
			return al.Genres[0].Name
		}
		if al.Genres[0].Value != "" {
			return al.Genres[0].Value
		}
	}
	return al.Genre
}

// GroupAlbumsByGenre partitions albums into curated genre buckets.
func GroupAlbumsByGenre(albums []Album) map[string][]Album {
	out := make(map[string][]Album)
	for _, al := range albums {
		b := ClassifyGenre(albumGenre(al))
		out[b] = append(out[b], al)
	}
	return out
}

// ---------- Selection ----------

// Track is a chosen song ready to be fetched and announced.
type Track struct {
	Title    string
	Artist   string
	Album    string
	AlbumID  string
	Year     int
	Genre    string
	SongID   string
	Duration int
	Suffix   string
}

// SelectTracks fetches the year's albums and picks up to n tracks with no genre
// filter (the "mix" selection).
func SelectTracks(ctx context.Context, nd *Subsonic, lf *LastFM, year, n int, excludeAlbums map[string]bool) ([]Track, error) {
	albums, err := nd.AlbumsByYear(ctx, year, 500)
	if err != nil {
		return nil, err
	}
	if len(albums) == 0 {
		return nil, fmt.Errorf("no albums found for year %d", year)
	}
	return SelectTracksFromAlbums(ctx, nd, lf, albums, n, excludeAlbums)
}

// SelectTracksFromAlbums picks up to n tracks from a pre-fetched album pool:
// shuffle (preferring albums not in excludeAlbums and deduplicating by artist),
// then pick the "most famous" track per album via Last.fm -> highest PlayCount
// -> first track. The input slice is shuffled in place.
func SelectTracksFromAlbums(ctx context.Context, nd *Subsonic, lf *LastFM, albums []Album, n int, excludeAlbums map[string]bool) ([]Track, error) {
	if len(albums) == 0 {
		return nil, fmt.Errorf("no albums available")
	}

	rand.Shuffle(len(albums), func(i, j int) { albums[i], albums[j] = albums[j], albums[i] })

	// Partition into fresh albums first, recently-played albums as reserve.
	var fresh, recent []Album
	for _, al := range albums {
		if excludeAlbums != nil && excludeAlbums[al.ID] {
			recent = append(recent, al)
		} else {
			fresh = append(fresh, al)
		}
	}
	ordered := append(fresh, recent...)

	var tracks []Track
	seenArtists := make(map[string]bool)
	for _, al := range ordered {
		if len(tracks) >= n {
			break
		}
		artistKey := normalize(al.Artist)
		if artistKey != "" && artistKey != "various artists" && seenArtists[artistKey] {
			continue
		}
		if tr, ok := pickMostFamous(ctx, nd, lf, al); ok {
			if artistKey != "" {
				seenArtists[artistKey] = true
			}
			tracks = append(tracks, tr)
		}
	}
	return tracks, nil
}

func pickMostFamous(ctx context.Context, nd *Subsonic, lf *LastFM, al Album) (Track, bool) {
	album, songs, err := nd.GetAlbum(ctx, al.ID)
	if err != nil || len(songs) == 0 {
		return Track{}, false
	}
	metaAlbum := al
	if album != nil {
		if metaAlbum.Artist == "" {
			metaAlbum.Artist = album.Artist
		}
		if len(metaAlbum.Genres) == 0 && len(album.Genres) > 0 {
			metaAlbum.Genres = album.Genres
		}
		if metaAlbum.Genre == "" && album.Genre != "" {
			metaAlbum.Genre = album.Genre
		}
	}

	sorted := sortAlbumSongs(songs)

	// 1. Last.fm match (first top track that appears on the album wins).
	artist := metaAlbum.Artist
	if (artist == "" || strings.EqualFold(artist, "Various Artists")) && sorted[0].Artist != "" {
		artist = sorted[0].Artist
	}
	if lf != nil && artist != "" {
		if top, err := lf.TopTracks(ctx, artist, 50); err == nil {
			for _, name := range top {
				for _, s := range sorted {
					if matchTitle(name, s.Title) {
						return toTrack(metaAlbum, s), true
					}
				}
			}
		}
	}

	// 2. Fallback: highest playCount on the album (if any track has been played).
	bestIdx := -1
	bestPlays := 0
	for i, s := range sorted {
		if s.PlayCount > bestPlays {
			bestPlays = s.PlayCount
			bestIdx = i
		}
	}
	if bestIdx >= 0 {
		return toTrack(metaAlbum, sorted[bestIdx]), true
	}

	// 3. Fallback: first track (by disc and track number).
	return toTrack(metaAlbum, sorted[0]), true
}

func sortAlbumSongs(songs []Song) []Song {
	sorted := append([]Song(nil), songs...)
	sort.Slice(sorted, func(i, j int) bool {
		di, dj := sorted[i].DiscNumber, sorted[j].DiscNumber
		if di <= 0 {
			di = 1
		}
		if dj <= 0 {
			dj = 1
		}
		if di != dj {
			return di < dj
		}
		ti, tj := sorted[i].Track, sorted[j].Track
		// Place untagged (Track <= 0) after numbered tracks.
		if (ti <= 0) != (tj <= 0) {
			return ti > 0
		}
		if ti != tj {
			return ti < tj
		}
		return sorted[i].Title < sorted[j].Title
	})
	return sorted
}

func toTrack(al Album, s Song) Track {
	genre := albumGenre(al)
	year := al.Year
	if year == 0 {
		year = s.Year
	}
	artist := s.Artist
	if artist == "" {
		artist = al.Artist
	}
	return Track{
		Title: s.Title, Artist: artist, Album: al.Name, AlbumID: al.ID, Year: year,
		Genre: genre, SongID: s.ID, Duration: s.Duration, Suffix: s.Suffix,
	}
}

// matchTitle compares two track titles case-, accent-, punctuation-, and
// parenthetical-suffix-insensitively.
func matchTitle(a, b string) bool {
	na, nb := normalize(a), normalize(b)
	return na != "" && na == nb
}

var parenSuffixRe = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)

var accentMap = map[rune]rune{
	'ά': 'α', 'έ': 'ε', 'ή': 'η', 'ί': 'ι', 'ό': 'ο', 'ύ': 'υ', 'ώ': 'ω',
	'Ά': 'α', 'Έ': 'ε', 'Ή': 'η', 'Ί': 'ι', 'Ό': 'ο', 'Ύ': 'υ', 'Ώ': 'ω',
	'ΐ': 'ι', 'ΰ': 'υ', 'ϊ': 'ι', 'ϋ': 'υ',
	'à': 'a', 'á': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a',
	'è': 'e', 'é': 'e', 'ê': 'e', 'ë': 'e',
	'ì': 'i', 'í': 'i', 'î': 'i', 'ï': 'i',
	'ò': 'o', 'ó': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o',
	'ù': 'u', 'ú': 'u', 'û': 'u', 'ü': 'u',
	'ñ': 'n', 'ç': 'c',
}

func normalize(s string) string {
	stripped := parenSuffixRe.ReplaceAllString(s, "")
	if strings.TrimSpace(stripped) == "" {
		stripped = s
	}
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(stripped) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if m, ok := accentMap[r]; ok {
			r = m
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastSpace = false
		} else if !lastSpace {
			b.WriteByte(' ')
			lastSpace = true
		}
	}
	return strings.TrimSpace(b.String())
}
