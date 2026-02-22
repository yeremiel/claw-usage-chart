# Claw Usage Chart

A lightweight local web dashboard for visualizing your [OpenClaw](https://github.com/openclaw/openclaw) token usage and API costs.

![Claw Usage Chart](docs/Screenshot.png)

## Features

- **Fast** — SQLite incremental cache keeps responses ~30 ms even after months of data
- **Single binary** — ships as one self-contained executable, no runtime needed
- **Live auto-refresh** — configurable interval (10s / 30s / 1m / 5m)
- **Date filters** — Today / 7d / 30d / All, or custom range
- **Per-agent & per-model breakdown** — tokens, cost, record count
- **Daily token trend chart**
- **Usage heatmap** — token activity by hour of day × day of week

## Build with Version

```bash
go build -ldflags "-X main.version=$(git describe --tags --always --dirty)" -o claw-usage-chart .
```

## Requirements

- Go 1.22+ (to build)
- OpenClaw installed and used at least once (session files in `~/.openclaw/agents/`)

## Quick Start

```bash
git clone https://github.com/yeremiel/claw-usage-chart.git
cd claw-usage-chart
go build -o claw-usage-chart .
./claw-usage-chart --open
```

`--open` 플래그를 사용하면 서버 시작 후 브라우저가 자동으로 열립니다. 생략하면 직접 http://localhost:8585 에 접속하세요.

## Configuration

### CLI Flags

| Flag | Short | Description |
|------|-------|-------------|
| `--port` | `-p` | 서버 포트 (기본: 8585) |
| `--host` | | 바인드 주소 (기본: 0.0.0.0) |
| `--daemon` | `-d` | 백그라운드 데몬으로 실행 |
| `--stop` | | 실행 중인 데몬 종료 |
| `--status` | | 데몬 실행 상태 확인 |
| `--open` | `-o` | 서버 시작 후 브라우저 열기 |
| `--reset` | | SQLite 캐시 삭제 후 시작 |
| `--version` | `-v` | 버전 출력 |

```bash
./claw-usage-chart -p 9000 --open          # 포트 9000, 브라우저 자동 열기
./claw-usage-chart --daemon --open         # 백그라운드 실행 + 브라우저
./claw-usage-chart --status                # 데몬 상태 확인
./claw-usage-chart --stop                  # 데몬 종료
./claw-usage-chart --reset                 # 캐시 초기화 후 시작
```

### Environment Variables

CLI 플래그가 지정되지 않았을 때 환경변수가 사용됩니다.

| Variable | Default | Description |
|---|---|---|
| `OCL_PORT` | `8585` | TCP port to listen on |
| `OCL_HOST` | `0.0.0.0` | Bind address |
| `OCL_AGENTS_DIR` | `~/.openclaw/agents` | Path to OpenClaw agents directory |
| `OCL_DB_PATH` | `<binary dir>/usage_cache.db` | Path to SQLite cache file |

```bash
OCL_PORT=9000 OCL_AGENTS_DIR=/custom/path ./claw-usage-chart
```

## How It Works

On every `/api/stats` request the server:

1. Checks each JSONL session file for newly-appended bytes (via stored byte offset)
2. Parses only the new lines and inserts them into SQLite
3. Aggregates from SQLite and returns JSON — no full re-scan

The first run builds the cache (a few seconds). Every subsequent call is fast regardless of how much historical data has accumulated.

The dashboard UI (`index.html`) and icon (`favicon.svg`) are embedded directly in the binary at build time — no extra files needed at runtime.

## Keep It Running

### Built-in Daemon Mode

```bash
./claw-usage-chart --daemon         # 백그라운드 실행
./claw-usage-chart --status         # 실행 상태 확인
./claw-usage-chart --stop           # 종료
```

### macOS launchd (자동 시작 / 크래시 재시작)

Create a launchd plist at `~/Library/LaunchAgents/com.openclaw.usage-dashboard.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.openclaw.usage-dashboard</string>
  <key>ProgramArguments</key>
  <array>
    <string>/path/to/claw-usage-chart</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/tmp/openclaw-dashboard.log</string>
  <key>StandardErrorPath</key>
  <string>/tmp/openclaw-dashboard.err</string>
</dict>
</plist>
```

Then load it:

```bash
launchctl load ~/Library/LaunchAgents/com.openclaw.usage-dashboard.plist
```

## File Structure

```
claw-usage-chart/
├── main.go       HTTP server, routing, graceful shutdown
├── cli.go        CLI flags, daemon management, browser open
├── db.go         SQLite incremental cache layer
├── parser.go     JSONL parser / usage extractor
├── index.html    Dashboard UI (Chart.js) — embedded in binary
├── favicon.svg   OpenClaw icon — embedded in binary
├── go.mod
└── .gitignore
```

## License

MIT

---

> Vibe-coded with [OpenClaw](https://github.com/openclaw/openclaw) 🤖 — README and all.
