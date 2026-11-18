# Audio Briefing 📻

An autonomous daily audio news briefing generator written in Go. Twice a day it fetches speech-ready news summaries, synthesizes neural speech, optionally interleaves music from any Subsonic-compatible streaming server, normalizes audio, and publishes a radio-style playlist alongside an embedded web player.

Music is optional: set `MUSIC_TRACKS=0` and the service produces a pure news briefing with no music source required.

---

## 🌐 Pipeline Context

`audio-briefing` is the orchestration and playback layer of a small pipeline:

```
                    ┌──────────────────────────────────────────────┐
                    │            daily-news-aggregator             │
                    │   (speech-ready news summaries :8080)        │
                    └───────────────────┬──────────────────────────┘
                                        │ GET /api/briefing-news
                                        ▼
┌──────────────────────┐      ┌──────────────────────────────────────┐
│  voice-service       │      │          audio-briefing (here)       │
│  (neural TTS :8002)  │◄─────│    (pipeline orchestrator :8380)     │
└──────────────────────┘  POST│                                      │
                          /synth└───────────────────┬────────────────┘
                                                    │ Subsonic REST API (optional)
                                                    ▼
                                     ┌────────────────────────────────┐
                                     │  any Subsonic-compatible server │
                                     │  (Navidrome, Gonic, Airsonic…)  │
                                     └────────────────────────────────┘
```

| Upstream | Protocol / Port | Endpoint | Role |
|---|---|---|---|
| [`daily-news-aggregator`](https://github.com/panospet/daily-news-aggregator) | HTTP (Basic Auth) `:8080` | `GET /api/briefing-news?slot=morning\|evening` (fallback `POST /api/briefing-news/generate`) | Fetches pre-generated, verified Greek news summaries. |
| [`voice-service`](https://github.com/panospet/voice-service) | HTTP (Basic Auth) `:8002` | `POST /synthesize` | Neural Greek speech for news segments and spoken track intros. |
| Subsonic-compatible server (optional) | Subsonic REST API `:4533` | `/rest/getAlbumList2`, `/rest/getAlbum`, `/rest/download` | Discovers albums from the target release year, lists tracks, and streams audio for transcoding. |
| Last.fm API (optional) | HTTPS | `artist.getTopTracks` | Picks the most popular track per album. |
| Telegram API (optional) | HTTPS | Bot API | Failure/degradation alerts. |

---

## 🛠️ How It Works

Generation runs automatically twice per day (`08:00` morning, `20:00` evening, Europe/Athens) or via manual trigger:

```
[ Scheduled trigger or POST /api/generate ]
                    │
                    ▼
  1. Fetch pre-generated news block
     └── GET /api/briefing-news?slot={slot}
     └── Validates the block is from today (Athens) and <= 6h old
     └── Diversifies categories; caps sports at 2 items, placed last
                    │
                    ▼
  2. Select music tracks (skipped when MUSIC_TRACKS=0)
     └── Queries the Subsonic API for albums from the target release year
     └── Groups albums into curated genre buckets (mix = all albums)
     └── Excludes albums played in the last 4 briefings
     └── Picks the "most famous" track via Last.fm → play count → first track
                    │
                    ▼
  3. Synthesize speech & transcode audio (per segment)
     ├── News clip:  voice-service → ffmpeg loudnorm (-16 LUFS) → news_{i}.mp3
     ├── Spoken intro (optional): voice-service → loudnorm → intro_{i}.mp3
     └── Music track (optional): Subsonic /rest/download → MP3 192k → music_{i}.mp3
                    │
                    ▼
  4. Atomic publishing & retention
     ├── Writes playlist.json in a temp dir, then renames atomically
     ├── Updates latest.json (monotonic, never regresses)
     ├── Prunes briefings older than RETENTION_DAYS (default 30)
     └── Sends Telegram status notification
```

---

## ✨ Features

- **Autonomous scheduling with catch-up**: wakes every 60s; catches up missed slots; 30-minute backoff on failure.
- **News-only mode**: `MUSIC_TRACKS=0` produces a music-free briefing and never contacts a music server.
- **Smart news diversification**: spreads topics, caps sports at 2 items at the tail.
- **Intelligent song selection**: combines local library metadata with Last.fm popularity to pick the most notable song per album.
- **Genre channels**: music is curated into buckets (`Mix`, `Electronic`, `Rock`, `Pop`, `Hip-Hop`, `Jazz`, `World`, `Classical`, `Other`); the player renders one button per available genre and swaps music while news stays fixed.
- **Smart TTS pacing**: distinguishes disk-cache hits (<500ms) from live synthesis, inserting pauses only when needed.
- **Broadcast-standard audio**: speech normalized to `-16 LUFS`; music transcoded to MP3 192k without loudnorm to preserve dynamics.
- **Atomic publishing**: generations happen in `.tmp` dirs and commit via rename, so a half-generated briefing is never served.
- **Built-in mobile web player**: responsive, autoplay, scrubber, keyboard shortcuts.
- **Telegram notifications**: alerts on completion, degraded runs, or failures.

---

## 🔌 API Reference

All endpoints except `/healthz` require HTTP Basic Auth (`AUTH_USERNAME` / `AUTH_PASSWORD`).

| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Web player page |
| `GET` | `/healthz` | Unauthenticated health check |
| `GET` | `/audio/{date}/{slot}/{file}` | Streams generated MP3 with byte-range support |
| `GET` | `/api/latest` | `playlist.json` of the newest briefing |
| `GET` | `/api/briefings` | Lists all generated briefings |
| `GET` | `/api/briefing?date=…&slot=…` | `playlist.json` for a specific briefing |
| `GET` | `/api/status` | Generator status (`idle`/`running`/`error`) |
| `POST` | `/api/generate?slot=morning\|evening` | Triggers background generation (same-origin only) |

Example `playlist.json`:

```json
{
  "date": "2026-11-18",
  "slot": "morning",
  "created": "2026-11-18T08:00:15+02:00",
  "default": "mix",
  "genres": [ { "id": "mix", "label": "Mix" } ],
  "items": [
    {
      "id": "n0",
      "type": "news",
      "title": "Νέο νομοσχέδιο για τις συγκοινωνίες",
      "subtitle": "Ελλάδα",
      "url": "/audio/2026-11-18/morning/news_0.mp3",
      "duration": 34.5
    },
    {
      "id": "m0",
      "type": "music",
      "title": "Solar Echoes",
      "subtitle": "Kite",
      "album_id": "ab-1234",
      "url": "/audio/2026-11-18/morning/music_mix_0.mp3",
      "duration": 218.0
    }
  ],
  "variants": { }
}
```

> In news-only mode there is no `default`, no `genres`, and every item is `"type": "news"`.

---

## 🔐 Security

- HTTP Basic Auth (constant-time) on everything except `GET /healthz`; refuses to start without credentials.
- Security headers (`X-Content-Type-Options`, `X-Frame-Options`, strict CSP).
- Same-origin enforcement on `POST /api/generate` to prevent CSRF.
- Path containment: `/audio/*` and API reads validate the path stays inside `AUDIO_BASE_DIR`.
- Hardened HTTP server (read/header/idle timeouts).

---

## ⚙️ Configuration

See [`.env.example`](.env.example) for a complete commented template.

| Variable | Default | Description |
|---|---|---|
| `AUDIO_PORT` | `8380` | HTTP port |
| `AUDIO_BIND` | `0.0.0.0` | Bind address |
| `AUDIO_BASE_DIR` | `/var/lib/audio-briefing` | Destination directory for briefings & audio |
| `RETENTION_DAYS` | `30` | Days of history to retain |
| `SCHEDULE_SLOTS` | `08:00,20:00` | Athens local times for scheduled runs |
| `NEWS_SEGMENTS` | `10` | Target number of news clips |
| `MUSIC_TRACKS` | `10` | Music tracks to interleave; `0` = news-only |
| `TRACK_INTROS` | `true` | Spoken intro before each music track |
| `DASHBOARD_URL` | `http://localhost:8080` | Base URL of `daily-news-aggregator` |
| `VOICE_SERVICE_URL` | `http://127.0.0.1:8002` | Base URL of `voice-service` |
| `SUBSONIC_URL` | `http://localhost:4533` | Any Subsonic-compatible server (`NAVIDROME_*` accepted as legacy alias) |
| `SUBSONIC_USER` | *(optional)* | Subsonic username |
| `SUBSONIC_PASS` | *(optional)* | Subsonic password |
| `LASTFM_API_KEY` | *(optional)* | Last.fm API key |
| `AUTH_USERNAME` / `AUTH_PASSWORD` | *(required)* | HTTP Basic auth |

---

## 🚀 Deployment

### Docker Compose

A standalone [`docker-compose.yml`](docker-compose.yml) is included. It builds the image and mounts an output volume:

```bash
cp .env.example .env   # fill in secrets
docker compose up -d
```

### Local development

Requires `ffmpeg` and `ffprobe` on the host:

```bash
export AUTH_USERNAME="admin"
export AUTH_PASSWORD="secret"
export DASHBOARD_URL="http://localhost:8080"
export VOICE_SERVICE_URL="http://127.0.0.1:8002"
export SUBSONIC_URL="http://localhost:4533"
export SUBSONIC_USER="user"
export SUBSONIC_PASS="pass"
export AUDIO_BASE_DIR="/tmp/audio-briefing"
go run cmd/briefing/main.go
```

For a news-only run, add `export MUSIC_TRACKS=0` (no Subsonic credentials needed).

---

## 🧪 Testing

```bash
go test ./... -v
node --test internal/server/web/player.test.js
go vet ./...
go build ./...
```

---

## 📄 License

MIT License. See repository for details.
