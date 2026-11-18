# AGENTS.md — audio-briefing

Guidance for AI agents and humans working in this repository.

## What this repo is

An autonomous personal audio briefing generator written in Go (Go 1.24):
- Orchestrates twice-daily news curation (`08:00` morning, `20:00` evening Europe/Athens time).
- Combines speech-ready Greek news summaries from `daily-news-aggregator` with optional music tracks from any Subsonic-compatible server (Navidrome, Gonic, Airsonic…).
- Normalizes speech with ffmpeg `loudnorm`, transcodes music to MP3 192k without loudnorm, and builds radio-style playlists (`news -> intro -> music`).
- Serves an embedded mobile web player and JSON APIs on port `8380`.

## Pipeline Context

`audio-briefing` consumes two required services and two optional ones:

```
 daily-news-aggregator (:8080)  ──> GET /api/briefing-news  ──┐
 voice-service (:8002)          ──> POST /synthesize (TTS)  ──┼──> audio-briefing (:8380)
 Subsonic server (:4533, opt)   ──> Subsonic API (music)    ──┘
 Last.fm (opt)                  ──> artist.getTopTracks
```

| Service | Port / Protocol | Purpose | Key Env Vars |
|---|---|---|---|
| **`daily-news-aggregator`** | `8080` HTTP | Reads pre-generated news block via `GET /api/briefing-news?slot=morning\|evening`; fallback `POST /api/briefing-news/generate`. | `DASHBOARD_URL`, `AUTH_USERNAME`, `AUTH_PASSWORD` |
| **`voice-service`** | `8002` HTTP | Synthesizes news leads and spoken track intros via `POST /synthesize`. | `VOICE_SERVICE_URL`, `TTS_VOICE_NEWS`, `TTS_VOICE_INTRO` |
| **Subsonic server** (optional) | `4533` HTTP (Subsonic) | Discovers albums (`getAlbumList2`), lists tracks (`getAlbum`), downloads audio (`download`). | `SUBSONIC_URL`, `SUBSONIC_USER`, `SUBSONIC_PASS` (`NAVIDROME_*` accepted as legacy alias) |
| **Last.fm API** (optional) | HTTPS | Selects the most famous song per album. | `LASTFM_API_KEY` |

## Ports & Auth

| Port | Bind | Protocol | Auth |
|---|---|---|---|
| `8380` | `0.0.0.0` (configurable via `AUDIO_BIND`) | HTTP/1.1 | HTTP Basic (`AUTH_USERNAME` / `AUTH_PASSWORD`) |

- `GET /healthz` is unauthenticated and returns `{"ok":true}`.
- `POST /api/generate` rejects cross-origin browser requests (`checkSameOrigin`).

## Core Invariants & Pipeline Rules

1. **Scheduling & Catch-Up (`internal/scheduler`)**:
   - Timezone `Europe/Athens` (`briefing.Athens`). Wakes every 60s; filesystem is the durable "already done" flag.
   - Morning slot does NOT catch up once the evening window has begun.
   - Failures back off 30 minutes (`retryAfter`).
2. **News Validation & Diversity (`internal/news`, `internal/briefing`)**:
   - Rejects stale news blocks (>6h or from a previous day); triggers on-demand generation fallback.
   - Sports (`Category == "Αθλητισμός"`) capped at 2 items, placed at the tail.
3. **Music Curation (`internal/music`)** — **optional**:
   - `MUSIC_TRACKS=0` disables music entirely: the generator never contacts a Subsonic server and emits a news-only playlist (no `default`, no `genres`, all items `"news"`).
   - With music enabled, queries albums from `releaseYear = 2026` via the Subsonic client, groups them into curated genre buckets, shuffles, and excludes albums from the last 4 briefings (`recentAlbumIDs`).
   - Track popularity: Last.fm top-tracks match → highest `playCount` → first track. Downloads audio straight to disk (`DownloadToFile`).
4. **TTS Pacing & Cache Awareness**:
   - `TTS.Synthesize()` returns `callDuration`; <500ms is a disk-cache hit (no pause), longer is a live synthesis (enforces `TTSPauseSeconds`).
5. **Audio Normalization (`internal/audio`)**:
   - Speech: `loudnorm=I=-16:TP=-1.5:LRA=11` to MP3 192k.
   - Music: transcode to MP3 192k without loudnorm.
6. **Atomic Publishing**:
   - Build in `finalDir + ".tmp" + nano`, then `os.Rename(tmpDir, finalDir)` only on success.
   - `latest.json` writes are atomic and monotonic (never regresses to an older slot).
   - Retention never deletes the day being published; startup `CleanupTmpDirs` removes orphaned temp dirs.
7. **Status & Alerting (`internal/briefing`)**:
   - Generation state owned by `Generator` (`TryStart`/`Status`/`Wait`), shared by server/scheduler/API.
   - Failed/degraded runs send Telegram alerts when tokens are set; never log secrets.

## Development & Testing

- **Dependencies**: stdlib only — no external Go modules. Do not add third-party packages unless asked.
- All env vars with defaults are documented in `.env.example` (`.env` is gitignored; never commit real secrets).
- Unit tests: `internal/{briefing,music,scheduler,server}/*_test.go` and `internal/server/web/player.test.js` (dependency-free Node tests).

```bash
go test ./...
node --test internal/server/web/player.test.js
go vet ./...
go build -o briefing cmd/briefing/main.go

# Local run (music):
export AUTH_USERNAME="admin"
export AUTH_PASSWORD="password"
export SUBSONIC_URL="http://localhost:4533"
export SUBSONIC_USER="user"
export SUBSONIC_PASS="pass"
go run cmd/briefing/main.go

# Local run (news-only, no music server):
export MUSIC_TRACKS=0
go run cmd/briefing/main.go
```

## Conventions

- With music, preserve the triplet structure `[news] -> [intro] -> [music]`; in news-only mode there are no intros or music.
- Keep ffmpeg arguments strictly separated: speech (with loudnorm) vs music (without loudnorm).
- Filesystem writes must stay inside `AUDIO_BASE_DIR` (`isWithinBaseDir`).
- Commit messages follow Conventional Commits (`feat:`, `fix:`, `refactor:`, `test:`, `docs:`, `chore:`).
- Timezone logic uses `briefing.Athens` (exported `time.Location`), never `time.Local`.
