# AGENTS.md — audio-briefing

Guidance for AI agents and humans working in this repository.

## What this repo is

An autonomous personal audio briefing and music podcast generator written in Go (Go 1.24):
- Orchestrates twice-daily news and music curation (`08:00` morning, `20:00` evening Europe/Athens time).
- Combines speech-ready Greek news summaries from `daily-news-aggregator` with newly released music tracks from `media-stack`'s Navidrome server.
- Normalizes speech with ffmpeg `loudnorm`, transcodes music to MP3 192k without loudnorm, and builds sequential radio-style playlists (`news -> intro -> music`).
- Serves an embedded mobile web player and JSON APIs at `http://localhost:8380` (publicly exposed at `https://newsbrief.panos.pet`).

## The 4-Service Ecosystem Context

`audio-briefing` is the central consumer that connects to the other 3 services:

```
 daily-news-aggregator (:8080)   ──> GET /api/briefing-news  ──┐
 media-stack Navidrome (:4533)   ──> Subsonic API (music)    ──┼──> audio-briefing (:8380)
 voice-service (:8002)           ──> POST /synthesize (TTS)  ──┘     (https://newsbrief.panos.pet)
```

| Service | Port / Protocol | Connection Purpose | Key Env Vars |
|---|---|---|---|
| **`daily-news-aggregator`** | `8080` HTTP | Reads pre-generated news block via `GET /api/briefing-news?slot=morning\|evening`. Fallback: `POST /api/briefing-news/generate`. | `DASHBOARD_URL` (default `http://localhost:8080`), `AUTH_USERNAME`, `AUTH_PASSWORD` |
| **`voice-service`** | `8002` HTTP | Synthesizes news leads and spoken track intros via `POST /synthesize`. | `VOICE_SERVICE_URL` (default `http://127.0.0.1:8002`), `AUTH_USERNAME`, `AUTH_PASSWORD`, `TTS_VOICE_NEWS`, `TTS_VOICE_INTRO` |
| **`media-stack` (Navidrome)** | `4533` HTTP (Subsonic) | Discovers 2026 albums (`getAlbumList2`), lists tracks (`getAlbum`), and downloads audio files (`download`). | `NAVIDROME_URL` (default `http://localhost:4533`), `NAVIDROME_USER`, `NAVIDROME_PASS` |
| **`media-stack` (Cloudflare)** | Cloudflare Tunnel | Exposes container `:8380` at `newsbrief.panos.pet` via `cloudflared-newsbrief` service in `media-stack/docker-compose.yml`. | — |
| **Last.fm API** | External HTTPS | Queries `artist.getTopTracks` to select the most famous song per album. | `LASTFM_API_KEY` |

## Ports & Auth

| Port | Bind | Protocol | Auth |
|---|---|---|---|
| `8380` | `0.0.0.0` (configurable via `AUDIO_BIND`) | HTTP/1.1 | HTTP Basic (`AUTH_USERNAME` / `AUTH_PASSWORD`) |

- **Health check**: `GET /healthz` is unauthenticated and returns `{"ok":true}`.
- **CSRF / Origin Check**: `POST /api/generate` rejects cross-origin browser requests (`checkSameOrigin`).

## Core Invariants & Pipeline Rules

1. **Scheduling & Catch-Up (`internal/scheduler`)**:
   - Runs in timezone `Europe/Athens` (`briefing.Athens`).
   - Wakes every 60 seconds. Catches up missed slots for the current day on startup (`alreadyGenerated` checks filesystem for `date/slot/playlist.json`).
   - Morning slot will NOT catch up once the evening window has arrived.
   - Failures back off for 30 minutes (`retryAfter`) to avoid tight retry storms on flaky TTS.
2. **News Validation & Diversity (`internal/news`, `internal/briefing`)**:
   - Rejects stale news blocks (>6h old or from a previous day). Triggers on-demand `/api/briefing-news/generate` fallback if stale or 404.
   - Sports news (`Category == "Αθλητισμός"`) is capped at at most 2 items and placed strictly at the tail of the briefing.
3. **Music Curation (`internal/music`)**:
   - Queries albums from `releaseYear = 2026`.
   - Shuffles albums and excludes albums present in the last 4 published briefings (`recentAlbumIDs`).
   - Resolves track popularity: Last.fm top tracks match -> highest `playCount` on album -> first track.
   - Downloads audio directly to disk (`DownloadToFile`) rather than buffering full audio files in memory.
4. **TTS Pacing & Cache Awareness**:
   - Calls `TTS.Synthesize()`, which returns `callDuration`.
   - If `callDuration < 500ms`, it was a disk-cache hit on `voice-service`. If longer, it was a real network synthesis, so it enforces `TTSPauseSeconds` (default 10s) before the next live synthesis to prevent Edge TTS dropouts.
5. **Audio Normalization (`internal/audio`)**:
   - **Speech clips**: Normalized with ffmpeg `loudnorm=I=-16:TP=-1.5:LRA=11` to MP3 192 kbps.
   - **Music tracks**: Transcoded directly to MP3 192 kbps (`-codec:a libmp3lame -b:a 192k -ar 44100 -ac 2`) **without loudnorm** (preserves master dynamics).
6. **Atomic Publishing**:
   - Build playlist and files inside `finalDir + ".tmp" + nano`.
   - Only on full completion, rename `os.Rename(tmpDir, finalDir)`, update `latest.json`, and prune historical briefings older than `RETENTION_DAYS` (30 days).

## Development & Testing

```bash
# Run unit tests:
go test ./...

# Build binary:
go build -o briefing cmd/briefing/main.go

# Run locally:
export AUTH_USERNAME="admin"
export AUTH_PASSWORD="password"
export NAVIDROME_USER="user"
export NAVIDROME_PASS="pass"
go run cmd/briefing/main.go
```

## Conventions

- Preserve the triplet structure: `[news] -> [intro] -> [music]`.
- Keep ffmpeg arguments strictly separated between speech (with loudnorm) and music (without loudnorm).
- Any filesystem writes must stay inside `AUDIO_BASE_DIR` (`isWithinBaseDir` security check).
