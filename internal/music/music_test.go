package music

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMatchTitleStripsParentheticalsAndAccents(t *testing.T) {
	if !matchTitle("Ήλιος & Θάλασσα (Remastered 2026)", "ηλιος θαλασσα") {
		t.Fatalf("expected titles to match after normalization")
	}
	if !matchTitle("Café del Mar [Radio Edit]", "Cafe del Mar") {
		t.Fatalf("expected Latin diacritics and bracketed suffix to normalize")
	}
}

func TestSortAlbumSongsRespectsDiscAndTrackNumbers(t *testing.T) {
	songs := []Song{
		{ID: "untagged", Title: "Bonus", Track: 0, DiscNumber: 1},
		{ID: "d2t1", Title: "Disc2 Track1", Track: 1, DiscNumber: 2},
		{ID: "d1t2", Title: "Disc1 Track2", Track: 2, DiscNumber: 1},
		{ID: "d1t1", Title: "Disc1 Track1", Track: 1, DiscNumber: 0}, // Disc 0 defaults to 1
	}
	sorted := sortAlbumSongs(songs)
	want := []string{"d1t1", "d1t2", "untagged", "d2t1"}
	for i, w := range want {
		if sorted[i].ID != w {
			t.Fatalf("index %d: got %s, want %s", i, sorted[i].ID, w)
		}
	}
}

func TestPickMostFamousPlayCountFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"subsonic-response": {
				"status": "ok",
				"album": {
					"id": "alb1",
					"name": "Test Album",
					"artist": "Test Artist",
					"year": 2026,
					"song": [
						{"id": "s1", "title": "Intro", "track": 1, "playCount": 1},
						{"id": "s2", "title": "Hit Single", "track": 4, "playCount": 19},
						{"id": "s3", "title": "Outro", "track": 8, "playCount": 3}
					]
				}
			}
		}`))
	}))
	defer srv.Close()

	nd := NewNavidrome(srv.URL, "u", "p")
	tr, ok := pickMostFamous(context.Background(), nd, nil, Album{ID: "alb1", Name: "Test Album", Artist: "Test Artist", Year: 2026})
	if !ok {
		t.Fatalf("expected track to be selected")
	}
	if tr.SongID != "s2" || tr.Title != "Hit Single" {
		t.Fatalf("expected highest playCount song s2, got %+v", tr)
	}
}

func TestSubsonicFailedStatusReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"subsonic-response": {
				"status": "failed",
				"error": {"code": 40, "message": "Wrong username or password"}
			}
		}`))
	}))
	defer srv.Close()

	nd := NewNavidrome(srv.URL, "bad", "creds")
	_, err := nd.AlbumsByYear(context.Background(), 2026, 50)
	if err == nil {
		t.Fatalf("expected error on Subsonic status=failed")
	}
}
