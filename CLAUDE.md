# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

A lightweight local web dashboard for visualizing [OpenClaw](https://github.com/openclaw/openclaw) token usage and API costs. Builds as a single Go binary; `index.html` and `favicon.svg` are embedded via `//go:embed`.

## Build & Run

```bash
go build -o claw-usage-chart .    # build
./claw-usage-chart                # run (default: http://localhost:8585)
go run .                          # run without producing a binary
```

Environment variables:
- `OCL_PORT` (default `8585`), `OCL_HOST` (default `0.0.0.0`)
- `OCL_AGENTS_DIR` (default `~/.openclaw/agents`) — path to OpenClaw JSONL session files
- `OCL_DB_PATH` (default `usage_cache.db` next to the binary)

## Architecture

Single package (`main`), three Go files:

- **main.go** — HTTP server, routing (`/`, `/favicon.svg`, `/api/stats`, `/health`), serves static files via `embed.FS`
- **parser.go** — walks `~/.openclaw/agents/<agent>/sessions/*.jsonl` (`IterSessionFiles`), parses each line into a `UsageRecord` (`ParseLine`). Handles multiple JSONL formats (camelCase/snake_case, nested `message.usage`, etc.)
- **db.go** — SQLite cache layer. Incremental sync via per-file byte-offset tracking (`Sync`), aggregation queries (`CollectStats`). Uses WAL mode.

**Request flow**: `/api/stats?start=&end=` → `CollectStats` → `Sync` (parse only new bytes → insert into SQLite) → aggregation queries (per-agent / per-model / daily / heatmap) → JSON response

## Dependencies

- `modernc.org/sqlite` — pure Go SQLite driver (no CGo required)
- Chart.js — frontend charting via CDN (inside `index.html`)

## SQLite Schema

```sql
-- Tracks the last read position per file
CREATE TABLE file_state (
    file_path   TEXT PRIMARY KEY,
    agent_name  TEXT    NOT NULL,
    last_offset INTEGER NOT NULL DEFAULT 0
);

-- Cached usage records
CREATE TABLE usage_records (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_name  TEXT    NOT NULL,
    model       TEXT    NOT NULL,
    date_key    TEXT    NOT NULL,  -- "YYYY-MM-DD" or "unknown"
    tokens      INTEGER NOT NULL,
    cost        REAL    NOT NULL DEFAULT 0.0,
    hour        INTEGER,           -- 0-23, local time (nullable)
    dow         INTEGER            -- 0=Mon .. 6=Sun (nullable)
);
```

## /api/stats Response Shape

```json
{
  "generated_at": "2026-02-17T14:00:00Z",
  "cached": true,
  "sync": { "new_records": 3, "synced_files": 2, "skipped_files": 266 },
  "summary": {
    "total_tokens": 12345678,
    "total_cost": 12.34,
    "usage_records": 7307,
    "session_files": 269,
    "agent_count": 3,
    "model_count": 5,
    "day_count": 14
  },
  "agent_totals": [{ "agent": "main", "tokens": 10000000, "cost": 10.0, "records": 6000 }],
  "model_totals": [{ "model": "claude-sonnet-4-5", "tokens": 8000000, "cost": 8.0, "records": 5000 }],
  "daily_tokens": [{ "date": "2026-02-17", "tokens": 500000, "cost": 0.5, "records": 200 }],
  "heatmap": [{ "dow": 0, "hour": 9, "tokens": 300000, "cost": 0.3 }]
}
```

Query params: `?start=YYYY-MM-DD&end=YYYY-MM-DD` (omit both for all-time data)

## JSONL Record Examples

The parser handles these main formats:

```jsonl
{"type":"assistant","timestamp":"2026-02-17T14:00:00.000Z","model":"claude-sonnet-4-5","costUsd":0.012,"message":{"usage":{"input_tokens":1000,"output_tokens":500,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0}}}
{"type":"result","timestamp":"2026-02-17T14:01:00.000Z","model":"claude-sonnet-4-5","costUsd":0.005,"usage":{"input_tokens":500,"output_tokens":200}}
```

`total_tokens` = `input_tokens + output_tokens + cache_read_input_tokens + cache_creation_input_tokens`  
The `usage` field may be at the record's top level or nested inside `message.usage`.

## Notes

- Editing `index.html` requires a rebuild (`go build`) to take effect due to `//go:embed`. Use `go run .` during UI iteration to skip the manual build step.
- When changing the SQLite schema, review the migration logic in `ensureSchema` (currently drops and rebuilds tables when `hour`/`dow` columns are missing).
- To reset the cache: `rm usage_cache.db` — the next run will re-parse all files from scratch.
- No test files exist yet. Add tests in `*_test.go` files.

## Git Workflow (Protected `main`)

- The `main` branch is protected and cannot be pushed to directly.
- All changes must be merged via Pull Request (PR) only.
- Required PR conversations must be resolved before merge.
- Do not bypass branch protection requirements.
- Force pushes and branch deletions on `main` are not allowed.
