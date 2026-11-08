# Audio Briefing 📻

An autonomous daily audio briefing and music podcast generator written in Go. Twice a day, it orchestrates news fetching, neural speech synthesis, music curation from a personal streaming server, audio normalization, and publishes a structured radio-style playlist alongside an embedded web player.

---

## 🌐 The 4-Service Ecosystem Context

`audio-briefing` acts as the **central pipeline orchestrator and consumer** within a 4-service ecosystem:

```
                      ┌────────────────────────────────────────────────────────┐
                      │                      media-stack                       │
                      │  ┌──────────────┐     ┌──────────────┐                 │
                      │  │  Navidrome   │     │  Tinyproxy   │                 │
                      │  │  (Port 4533) │     │  (Port 8888) │                 │
                      │  └──────┬───────┘     └──────┬───────┘                 │
                      │         │ Subsonic API       │ Tunnel via CF Access    │
                      │         │                    ▼ (tinyproxy.panos.pet)   │
                      │         │             ┌──────────────────────────────┐ │
                      │         │             │    daily-news-aggregator     │ │
                      │         │             │  (Miniflux + AI Clustering + │ │
                      │         │             │   Grounded Summaries :8080)  │ │
                      │         │             └──────────────┬───────────────┘ │
                      │         │                            │                 │
                      │         │                            │ GET /api/       │
                      │         │                            │ briefing-news   │
                      │         ▼                            ▼                 │
                      │  ┌──────────────────────────────────────────────┐      │
                      │  │         audio-briefing (This repo)           │      │
                      │  │       (Pipeline Orchestrator & Player :8380) │      │
                      │  └──────────────────────┬───────────────────────┘      │
                      │                         │                              │
                      │                         │ POST /synthesize             │
                      │                         ▼                              │
                      │  ┌──────────────────────────────────────────────┐      │
                      │  │                voice-service                 │      │
                      │  │    (Neural Greek TTS & Web Scraper :8002)    │      │
                      │  └──────────────────────────────────────────────┘      │
                      │                         │                              │
                      │                         ▼                              │
                      │             newsbrief.panos.pet (Cloudflare Tunnel)    │
                      └────────────────────────────────────────────────────────┘
```

### Inter-Service Communication Details

| Target Service | Protocol / Port | Endpoint | Role & Interaction |
|---|---|---|---|
| **[`daily-news-aggregator`](https://github.com/panospet/daily-news-aggregator)** | HTTP (Basic Auth)<br>`:8080` | `GET /api/briefing-news?slot=morning\|evening`<br>`POST /api/briefing-news/generate?slot=...` (fallback) | Fetches the pre-generated, verified Greek news summaries. If missing or stale (>6h), triggers on-demand generation. |
| **[`voice-service`](https://github.com/panospet/voice-service)** | HTTP (Basic Auth)<br>`:8002` | `POST /synthesize` | Generates neural Greek speech for news segments (`el-GR-AthinaNeural`/`el-GR-NestorasNeural`) and track intros ("Επόμενο κομμάτι..."). |
| **[`media-stack`](https://github.com/panospet/media-stack) (Navidrome)** | Subsonic REST API<br>`:4533` | `/rest/getAlbumList2`<br>`/rest/getAlbum`<br>`/rest/download` | Queries new album releases (e.g., year 2026), parses tracklists, and streams full audio files for transcoding. |
| **[`media-stack`](https://github.com/panospet/media-stack) (Cloudflare)** | Cloudflare Tunnel | Host network `:8380` → `newsbrief.panos.pet` | Exposes the web player and audio stream securely to mobile devices and remote browsers without open router ports. |
| **Last.fm API** | HTTPS External | `ws.audioscrobbler.com` | Queries `artist.getTopTracks` to determine the "most famous" track for each selected album. |
| **Telegram API** | HTTPS External | Telegram Bot API | Sends push notifications on briefing generation completion, degraded states, or pipeline errors. |

---

## 🛠️ How It Works (Pipeline Architecture)

The generation pipeline executes automatically twice per day (`08:00` morning and `20:00` evening, Europe/Athens time) or via manual trigger:

```
[ Scheduled Trigger or POST /api/generate ]
                    │
                    ▼
  1. Fetch Pre-Generated News Block
     └── GET http://localhost:8080/api/briefing-news?slot={slot}
     └── Validates generation date is today (Athens) and age <= 6 hours
     └── Diversifies categories; caps sports to max 2 items, placed at the end
                    │
                    ▼
  2. Select Music Tracks
     └── Queries Navidrome Subsonic API for albums from target release year (2026)
     └── Shuffles albums, excluding albums played in the last 4 briefings
     └── Queries Last.fm for artist top tracks to match the "most famous" song
     └── Fallback: Highest play count on album -> First track
                    │
                    ▼
  3. Synthesize Speech & Transcode Audio (Triplets Loop)
     For each segment (default 10 triplets):
     ├── News Clip:
     │   ├── voice-service: POST /synthesize (news summary lead)
     │   └── ffmpeg: EBU R128 loudnorm (-16 LUFS) -> news_{i}.mp3
     ├── Spoken Intro (Optional):
     │   ├── voice-service: POST /synthesize ("Επόμενο κομμάτι: {title} από {artist}...")
     │   └── ffmpeg: EBU R128 loudnorm (-16 LUFS) -> intro_{i}.mp3
     └── Music Track:
         ├── Navidrome: Streams original audio via Subsonic /rest/download
         └── ffmpeg: Transcode to MP3 192 kbps (no loudnorm, preserves dynamics) -> music_{i}.mp3
                    │
                    ▼
  4. Atomic Publishing & Retention
     ├── Writes playlist.json inside temporary directory (e.g., 2026-11-08/morning.tmp123/)
     ├── Atomically renames tmp directory to final target directory
     ├── Updates latest.json symlink/pointer
     ├── Prunes historical briefings older than RETENTION_DAYS (default 30 days)
     └── Sends Telegram status notification
```

---

## ✨ Features

- **Autonomous Scheduling with Catch-Up**: Scheduler wakes every 60 seconds. If a scheduled window has passed today and has not been generated, it triggers catch-up automatically. Failed runs apply a 30-minute backoff.
- **Smart News Diversification**: Partitions news stories across topics, strictly limiting sports stories to at most 2 items and ordering them at the tail of the program.
- **Intelligent Song Selection**: Combines local library metadata with Last.fm listener data to curate the most notable song from each album.
- **Smart TTS Pacing & Disk Cache Awareness**: Tracks `voice-service` call durations to distinguish immediate disk-cache hits (<500ms) from live network generation, inserting polite pauses (`TTS_PAUSE_SECONDS`) only when necessary to prevent upstream throttling.
- **Broadcast-Standard Audio Normalization**:
  - News and introductions are normalized to `-16 LUFS` / `-1.5 dBFS TP` using ffmpeg's `loudnorm` filter.
  - Music is transcoded directly to 192 kbps MP3 without `loudnorm` to prevent dynamic compression on professionally mastered songs.
- **Atomic File Operations**: Generations occur in isolated `.tmp<timestamp>` directories and are committed atomically via filesystem renames, preventing corrupt or half-generated briefings from being served.
- **Durable Retention**: Retains 30 days of briefings, automatically cleaning up old directories on disk.
- **Built-in Mobile Web Player**: Embedded responsive web player with autoplay, scrubber, track listing, and keyboard shortcuts.
- **Telegram Notifications**: Real-time alerts on completion, degraded runs (e.g. 8/10 segments), or pipeline failures.

---

## 🔌 API Reference

All endpoints (except `/healthz`) require HTTP Basic Authentication (`AUTH_USERNAME` / `AUTH_PASSWORD`).

### Web Interface & Audio
| Method | Path | Description |
|---|---|---|
| `GET` | `/` | Responsive web player HTML page |
| `GET` | `/healthz` | Unauthenticated health check endpoint (`{"ok": true}`) |
| `GET` | `/audio/{date}/{slot}/{file}` | Streams generated MP3 files (`audio/mpeg`) with byte-range support |

### Management & Data APIs
| Method | Path | Description |
|---|---|---|
| `GET` | `/api/latest` | Returns the `playlist.json` of the most recently published briefing |
| `GET` | `/api/briefings` | Lists all available generated briefings grouped by date and slot |
| `GET` | `/api/briefing?date=YYYY-MM-DD&slot=morning\|evening` | Retrieves `playlist.json` for a specific date and slot |
| `GET` | `/api/status` | Current generator status (`idle`, `running`, `error`), last run timestamp, last error |
| `POST`| `/api/generate?slot=morning\|evening` | Triggers background generation for a slot (requires same-origin / fetch header) |

#### Example `playlist.json` Schema:
```json
{
  "date": "2026-11-08",
  "slot": "morning",
  "created": "2026-11-08T08:00:15+02:00",
  "items": [
    {
      "id": "n0",
      "type": "news",
      "title": "Νέο νομοσχέδιο για τις συγκοινωνίες",
      "subtitle": "Ελλάδα",
      "url": "/audio/2026-11-08/morning/news_0.mp3",
      "duration": 34.5
    },
    {
      "id": "i0",
      "type": "intro",
      "title": "Solar Echoes",
      "subtitle": "Kite",
      "url": "/audio/2026-11-08/morning/intro_0.mp3",
      "duration": 4.2
    },
    {
      "id": "m0",
      "type": "music",
      "title": "Solar Echoes",
      "subtitle": "Kite",
      "album_id": "ab-1234",
      "url": "/audio/2026-11-08/morning/music_0.mp3",
      "duration": 218.0
    }
  ]
}
```

---

## ⚙️ Configuration & Environment Variables

| Variable | Default | Description |
|---|---|---|
| `AUDIO_PORT` | `8380` | Port for the HTTP server |
| `AUDIO_BIND` | `0.0.0.0` | Bind interface IP |
| `AUDIO_BASE_DIR` | `/media/panos/Moof/audio-briefing` | Destination directory on disk for briefings & audio files |
| `RETENTION_DAYS` | `30` | Days of historical briefings to retain on disk |
| `SCHEDULE_SLOTS` | `08:00,20:00` | Comma-separated Athens local times for scheduled runs |
| `NEWS_SEGMENTS` | `10` | Target number of news clips per briefing |
| `MUSIC_TRACKS` | `10` | Target number of music tracks per briefing |
| `NEWS_SPEECH_MAX_CHARS` | `0` | Truncate news text to first sentence after N chars (0 = no limit) |
| `TRACK_INTROS` | `true` | Whether to generate spoken DJ intros before music tracks |
| `TTS_PAUSE_SECONDS` | `10` | Pause duration between network TTS calls to avoid throttling |
| `TTS_VOICE_NEWS` | `el-GR-NestorasNeural` | Voice identifier for news narration |
| `TTS_VOICE_INTRO` | `el-GR-NestorasNeural` | Voice identifier for track intros |
| `DASHBOARD_URL` | `http://localhost:8080` | Base URL of `daily-news-aggregator` |
| `VOICE_SERVICE_URL` | `http://127.0.0.1:8002` | Base URL of `voice-service` |
| `NAVIDROME_URL` | `http://localhost:4533` | Base URL of Navidrome Subsonic server |
| `NAVIDROME_USER` | *(required)* | Navidrome Subsonic username |
| `NAVIDROME_PASS` | *(required)* | Navidrome Subsonic password |
| `LASTFM_API_KEY` | *(optional)* | Last.fm API key for popularity lookups |
| `AUTH_USERNAME` | *(required)* | HTTP Basic authentication username |
| `AUTH_PASSWORD` | *(required)* | HTTP Basic authentication password |
| `TELEGRAM_BOT_TOKEN` | *(optional)* | Telegram Bot token for alerts |
| `TELEGRAM_CHAT_ID` | *(optional)* | Telegram Chat ID for alerts |

---

## 🚀 Deployment

### Option A: Via `media-stack` (Recommended)
`audio-briefing` is configured in `media-stack/docker-compose.yml` with host networking:
```yaml
audio-briefing:
  build:
    context: ../audio-briefing
  container_name: audio-briefing
  user: "1000:1000"
  network_mode: host
  restart: unless-stopped
  depends_on:
    - voice-service
    - navidrome
  environment:
    - TZ=Europe/Athens
    - AUDIO_BIND=0.0.0.0
    - AUDIO_PORT=8380
    - AUDIO_BASE_DIR=/data
    - DASHBOARD_URL=http://localhost:8080
    - VOICE_SERVICE_URL=http://127.0.0.1:8002
    - NAVIDROME_URL=http://localhost:4533
    - AUTH_USERNAME=${AUTH_USERNAME}
    - AUTH_PASSWORD=${AUTH_PASSWORD}
    - NAVIDROME_USER=${NAVIDROME_USER}
    - NAVIDROME_PASS=${NAVIDROME_PASS}
    - LASTFM_API_KEY=${ND_LASTFM_APIKEY}
    - TELEGRAM_BOT_TOKEN=${TELEGRAM_BOT_TOKEN}
    - TELEGRAM_CHAT_ID=${TELEGRAM_CHAT_ID}
  volumes:
    - ${BASE_DIR}/audio-briefing:/data
```

Start service:
```bash
cd ~/media-stack
docker compose up -d audio-briefing
```

### Option B: Local Development
Requires `ffmpeg` and `ffprobe` installed on host:
```bash
# Verify ffmpeg
ffmpeg -version

# Set environment
export AUTH_USERNAME="admin"
export AUTH_PASSWORD="secretpassword"
export NAVIDROME_USER="panos"
export NAVIDROME_PASS="navisecret"
export AUDIO_BASE_DIR="/tmp/audio-briefing"

# Run service
go run cmd/briefing/main.go
```

---

## 🧪 Testing

Run all unit tests covering scheduling, subsonic clients, music track picking, and server handlers:
```bash
go test ./... -v
```

---

## 📄 License

MIT License. See repository for details.
