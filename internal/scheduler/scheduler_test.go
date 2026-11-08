package scheduler

import "testing"

func TestParseHHMM(t *testing.T) {
	valid := map[string]int{
		"00:00": 0,
		"08:00": 480,
		"18:30": 1110,
		"23:59": 1439,
	}
	for in, want := range valid {
		got, err := parseHHMM(in)
		if err != nil || got != want {
			t.Fatalf("parseHHMM(%q) = (%d, %v), want (%d, nil)", in, got, err, want)
		}
	}

	invalid := []string{"", "08", "24:00", "08:60", "-1:00", "ab:cd"}
	for _, in := range invalid {
		if _, err := parseHHMM(in); err == nil {
			t.Fatalf("parseHHMM(%q) expected error, got nil", in)
		}
	}
}
