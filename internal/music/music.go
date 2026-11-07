package music

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ---------- Navidrome ----------

// Navidrome is a minimal Subsonic API client for discovery + audio download.
type Navidrome struct {
	baseURL string
	user    string
	pass    string
	client  *http.Client
}

func NewNavidrome(baseURL, user, pass string) *Navidrome {
	return &Navidrome{baseURL: baseURL, user: user, pass: pass, client: &http.Client{Timeout: 180 * time.Second}}
}

func (n *Navidrome) auth() url.Values {
	salt := make([]byte, 6)
	_, _ = rand.Read(salt)
	saltStr := hex.EncodeToString(salt)[:6]
	hash := md5.Sum([]byte(n.pass + saltStr))
	return url.Values{
		"u": {n.user}, "t": {hex.EncodeToString(hash[:])}, "s": {saltStr},
		"v": {"1.16.1"}, "c": {"audio-briefing"}, "f": {"json"},
	}
}

func (n *Navidrome) get(ctx context.Context, endpoint string, params url.Values) ([]byte, error) {
	q := n.auth()
	for k, vs := range params {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u := fmt.Sprintf("%s/rest/%s?%s", n.baseURL, endpoint, q.Encode())
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
		return nil, fmt.Errorf("navidrome %s: status %d", endpoint, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// Genre is a genre tag object as returned by Navidrome.
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
	Genres    []Genre `json:"genres"`
	SongCount int     `json:"songCount"`
}

// Song is a track from getAlbum.
type Song struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Track    int    `json:"track"`
	Duration int    `json:"duration"`
	Year     int    `json:"year"`
	Suffix   string `json:"suffix"`
	Path     string `json:"path"`
}

type subsonic struct {
	Status     string      `json:"status"`
	AlbumList2 albumList2  `json:"albumList2"`
	Album      albumDetail `json:"album"`
}

type albumList2 struct {
	Album []Album `json:"album"`
}

type albumDetail struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Artist string  `json:"artist"`
	Year   int     `json:"year"`
	Genres []Genre `json:"genres"`
	Song   []Song  `json:"song"`
}

// AlbumsByYear returns albums released in the given year.
func (n *Navidrome) AlbumsByYear(ctx context.Context, year, size int) ([]Album, error) {
	data, err := n.get(ctx, "getAlbumList2", url.Values{
		"type": {"byYear"}, "fromYear": {fmt.Sprint(year)}, "toYear": {fmt.Sprint(year)}, "size": {fmt.Sprint(size)},
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
	return res.SubsonicResponse.AlbumList2.Album, nil
}

// GetAlbum returns the album and its songs.
func (n *Navidrome) GetAlbum(ctx context.Context, id string) (*Album, []Song, error) {
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
	a := res.SubsonicResponse.Album
	album := &Album{ID: a.ID, Name: a.Name, Artist: a.Artist, Year: a.Year, Genres: a.Genres}
	return album, a.Song, nil
}

// Download returns the original audio bytes for a song (Navidrome resolves the
// real file location, so this is robust against on-disk renames).
func (n *Navidrome) Download(ctx context.Context, songID string) ([]byte, error) {
	return n.get(ctx, "download", url.Values{"id": {songID}})
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
	u := "http://ws.audioscrobbler.com/2.0/?" + q.Encode()
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

// ---------- Selection ----------

// Track is a chosen song ready to be fetched and announced.
type Track struct {
	Title    string
	Artist   string
	Album    string
	Year     int
	Genre    string
	SongID   string
	Duration int
	Suffix   string
}

// SelectTracks picks up to n tracks: shuffle the year's albums, then pick the
// "most famous" track per album via Last.fm (fallback: first track).
func SelectTracks(ctx context.Context, nd *Navidrome, lf *LastFM, year, n int) ([]Track, error) {
	albums, err := nd.AlbumsByYear(ctx, year, 500)
	if err != nil {
		return nil, err
	}
	if len(albums) == 0 {
		return nil, fmt.Errorf("no albums found for year %d", year)
	}

	rand.Shuffle(len(albums), func(i, j int) { albums[i], albums[j] = albums[j], albums[i] })

	var tracks []Track
	for _, al := range albums {
		if len(tracks) >= n {
			break
		}
		if tr, ok := pickMostFamous(ctx, nd, lf, al); ok {
			tracks = append(tracks, tr)
		}
	}
	return tracks, nil
}

func pickMostFamous(ctx context.Context, nd *Navidrome, lf *LastFM, al Album) (Track, bool) {
	album, songs, err := nd.GetAlbum(ctx, al.ID)
	if err != nil || len(songs) == 0 {
		return Track{}, false
	}
	artist := al.Artist
	if artist == "" && album != nil && album.Artist != "" {
		artist = album.Artist
	}

	sorted := append([]Song(nil), songs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Track != sorted[j].Track {
			return sorted[i].Track < sorted[j].Track
		}
		return sorted[i].Title < sorted[j].Title
	})

	// 1. Last.fm match (first top track that appears on the album wins).
	if lf != nil {
		if top, err := lf.TopTracks(ctx, artist, 50); err == nil {
			for _, name := range top {
				for _, s := range sorted {
					if matchTitle(name, s.Title) {
						return toTrack(al, s), true
					}
				}
			}
		}
	}

	// 2. Fallback: first track (by track number).
	return toTrack(al, sorted[0]), true
}

func toTrack(al Album, s Song) Track {
	genre := ""
	if len(al.Genres) > 0 {
		genre = al.Genres[0].Name
		if genre == "" {
			genre = al.Genres[0].Value
		}
	}
	year := al.Year
	if year == 0 {
		year = s.Year
	}
	return Track{
		Title: s.Title, Artist: s.Artist, Album: al.Name, Year: year,
		Genre: genre, SongID: s.ID, Duration: s.Duration, Suffix: s.Suffix,
	}
}

// matchTitle compares two track titles case- and accent-insensitively.
func matchTitle(a, b string) bool {
	return normalize(a) == normalize(b)
}

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
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.Is(unicode.Mn, r) {
			continue
		}
		if m, ok := accentMap[r]; ok {
			b.WriteRune(m)
		} else {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
