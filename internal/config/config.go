package config

import (
	"os"
	"strconv"
	"strings"
)

// Config holds all configuration for the audio-briefing service.
type Config struct {
	Port           int
	Bind           string
	BaseDir        string
	RetentionDays  int
	NewsSegments   int
	MusicTracks    int
	NewsSpeechMaxChars int
	TTSVoiceNews   string
	TTSVoiceIntro  string
	TrackIntros    bool
	ScheduleSlots  []string
	DashboardURL   string
	AuthUsername   string
	AuthPassword   string
	NavidromeURL   string
	NavidromeUser  string
	NavidromePass  string
	LastFMAPIKey   string
	VoiceServiceURL string
	TelegramToken  string
	TelegramChatID string
}

// Load reads configuration from environment variables with sensible defaults.
func Load() *Config {
	return &Config{
		Port:           envInt("AUDIO_PORT", 8380),
		Bind:           env("AUDIO_BIND", "0.0.0.0"),
		BaseDir:        env("AUDIO_BASE_DIR", "/media/panos/Moof/audio-briefing"),
		RetentionDays:  envInt("RETENTION_DAYS", 30),
		NewsSegments:   envInt("NEWS_SEGMENTS", 10),
		MusicTracks:    envInt("MUSIC_TRACKS", 10),
		NewsSpeechMaxChars: envInt("NEWS_SPEECH_MAX_CHARS", 500),
		TTSVoiceNews:   env("TTS_VOICE_NEWS", "el-GR-AthinaNeural"),
		TTSVoiceIntro:  env("TTS_VOICE_INTRO", "el-GR-NestorasNeural"),
		TrackIntros:    envBool("TRACK_INTROS", true),
		ScheduleSlots:  envList("SCHEDULE_SLOTS", "08:00,20:00"),
		DashboardURL:   strings.TrimRight(env("DASHBOARD_URL", "http://localhost:8080"), "/"),
		AuthUsername:   env("AUTH_USERNAME", ""),
		AuthPassword:   env("AUTH_PASSWORD", ""),
		NavidromeURL:   strings.TrimRight(env("NAVIDROME_URL", "http://localhost:4533"), "/"),
		NavidromeUser:  env("NAVIDROME_USER", ""),
		NavidromePass:  env("NAVIDROME_PASS", ""),
		LastFMAPIKey:   env("LASTFM_API_KEY", env("ND_LASTFM_APIKEY", "")),
		VoiceServiceURL: strings.TrimRight(env("VOICE_SERVICE_URL", "http://127.0.0.1:8002"), "/"),
		TelegramToken:  env("TELEGRAM_BOT_TOKEN", ""),
		TelegramChatID: env("TELEGRAM_CHAT_ID", ""),
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}

func envList(k, def string) []string {
	raw := env(k, def)
	parts := strings.Split(raw, ",")
	out := parts[:0]
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
