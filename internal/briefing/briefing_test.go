package briefing

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/panos/audio-briefing/internal/music"
	"github.com/panos/audio-briefing/internal/news"
)

func TestTruncateLeadHandlesGreekNumbersAndAbbreviations(t *testing.T) {
	text := "Το πρόγραμμα ύψους 60.000 ευρώ αφορά π.χ. μικρές επιχειρήσεις στην Αττική και τη Θεσσαλονίκη. Η δεύτερη πρόταση ολοκληρώνει το απόσπασμα κανονικά. Η τρίτη πρόταση είναι πολύ μεγάλη και πρέπει να κοπεί επειδή ξεπερνά το όριο χαρακτήρων που έχουμε θέσει."
	got := truncateLead(text, 160)
	if strings.HasSuffix(got, "60.") || strings.HasSuffix(got, "π.χ.") {
		t.Fatalf("unexpected cut at number or abbreviation: %q", got)
	}
	if !strings.HasSuffix(got, "κανονικά.") {
		t.Fatalf("expected cut at end of second sentence, got %q", got)
	}
}

func TestTruncateLeadWordBoundaryFallback(t *testing.T) {
	text := "Μία πολύ μεγάλη πρόταση χωρίς καμία τελεία που συνεχίζει αδιάκοπα για πολλές λέξεις ώστε να δοκιμάσουμε το κόψιμο σε όριο λέξης αντί στη μέση μιας λέξης"
	got := truncateLead(text, 80)
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis on word-boundary fallback, got %q", got)
	}
	if strings.Contains(got, "λέξ…") {
		t.Fatalf("expected cut on whitespace boundary, got %q", got)
	}
}

func TestSelectDiverseNewsCapsCategoryAndDeduplicatesTitles(t *testing.T) {
	in := []news.Item{
		{Topic: "Νέα μέτρα στήριξης στην οικονομία", Category: "Οικονομία"},
		{Topic: "Μέτρα στήριξης στην ελληνική οικονομία σήμερα", Category: "Ελλάδα"}, // shares >=2 words ("μέτρα", "στήριξης", "οικονομία")
		{Topic: "Αύξηση εξαγωγών το πρώτο τρίμηνο", Category: "Οικονομία"},
		{Topic: "Τρίτο θέμα για επιτόκια τραπεζών", Category: "Οικονομία"}, // 3rd "Οικονομία" -> overflow
		{Topic: "Σημαντική νίκη στον τελικό κυπέλλου", Category: "Αθλητισμός"},
	}
	out := selectDiverseNews(in)
	if len(out) != len(in) {
		t.Fatalf("expected %d items preserved (primary + overflow), got %d", len(in), len(out))
	}
	// Primary 3 items should be index 0, 2, 4; duplicate (1) and 3rd Οικονομία (3) moved to tail.
	if out[0].Topic != in[0].Topic || out[1].Topic != in[2].Topic || out[2].Topic != in[4].Topic {
		t.Fatalf("unexpected primary ordering: %+v", out)
	}
}

func TestBuildIntroTextOmitsYearAndDuplicateAlbum(t *testing.T) {
	trSame := music.Track{
		Title:  "Midnight Sun",
		Artist: "Test Artist",
		Album:  "Midnight Sun",
		Year:   2026,
		Genre:  "Indie Rock",
	}
	gotSame := buildIntroText(trSame)
	if strings.Contains(gotSame, "2026") {
		t.Fatalf("expected year to be omitted, got %q", gotSame)
	}
	if strings.Contains(gotSame, "Από το άλμπουμ") {
		t.Fatalf("expected duplicate album name to be omitted, got %q", gotSame)
	}
	if !strings.Contains(gotSame, "Indie Rock") {
		t.Fatalf("expected genre to be preserved, got %q", gotSame)
	}

	trDiff := music.Track{
		Title:  "Track One",
		Artist: "Test Artist",
		Album:  "Great Album",
		Year:   2026,
		Genre:  "Synthpop",
	}
	gotDiff := buildIntroText(trDiff)
	if !strings.Contains(gotDiff, "Από το άλμπουμ Great Album, Synthpop.") {
		t.Fatalf("expected album + genre without year, got %q", gotDiff)
	}
}

func TestWriteLatestDoesNotRegressNewerSlot(t *testing.T) {
	dir := t.TempDir()
	writeLatest(dir, "2026-11-07", "evening")
	writeLatest(dir, "2026-11-07", "morning")

	b, err := os.ReadFile(filepath.Join(dir, "latest.json"))
	if err != nil {
		t.Fatalf("read latest.json: %v", err)
	}
	var l Latest
	if err := json.Unmarshal(b, &l); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if l.Slot != "evening" {
		t.Fatalf("expected latest.json to stay on evening, got %q", l.Slot)
	}
}
