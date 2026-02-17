# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 프로젝트 개요

OpenClaw 토큰 사용량과 API 비용을 시각화하는 로컬 웹 대시보드. 단일 Go 바이너리로 빌드되며, `index.html`과 `favicon.svg`는 `//go:embed`로 바이너리에 포함된다.

## 빌드 및 실행

```bash
go build -o claw-usage-chart .    # 빌드
./claw-usage-chart                # 실행 (기본 http://localhost:8585)
go run .                          # 빌드 없이 실행
```

환경 변수로 설정 변경:
- `OCL_PORT` (기본 `8585`), `OCL_HOST` (기본 `0.0.0.0`)
- `OCL_AGENTS_DIR` (기본 `~/.openclaw/agents`) — JSONL 세션 파일 위치
- `OCL_DB_PATH` (기본 바이너리 디렉터리의 `usage_cache.db`)

## 아키텍처

단일 패키지(`main`), 3개 Go 파일로 구성:

- **main.go** — HTTP 서버, 라우팅(`/`, `/favicon.svg`, `/api/stats`, `/health`), `embed.FS`로 정적 파일 제공
- **parser.go** — `~/.openclaw/agents/<agent>/sessions/*.jsonl` 파일 탐색(`IterSessionFiles`), JSONL 한 줄을 `UsageRecord`로 파싱(`ParseLine`). 다양한 JSONL 포맷(camelCase/snake_case, 중첩 message 등)을 유연하게 처리
- **db.go** — SQLite 캐시 계층. 파일별 바이트 오프셋 추적으로 증분 동기화(`Sync`), 집계 쿼리(`CollectStats`). WAL 모드 사용

**요청 흐름**: `/api/stats?start=&end=` → `CollectStats` → `Sync`(새 바이트만 파싱→SQLite 삽입) → 집계 쿼리(에이전트별/모델별/일별/히트맵) → JSON 응답

## 주요 의존성

- `modernc.org/sqlite` — 순수 Go SQLite 드라이버 (CGo 불필요)
- Chart.js — 프론트엔드 차트 (index.html 내 CDN)

## SQLite 스키마

```sql
-- 파일별 마지막 읽기 위치 추적
CREATE TABLE file_state (
    file_path   TEXT PRIMARY KEY,
    agent_name  TEXT    NOT NULL,
    last_offset INTEGER NOT NULL DEFAULT 0
);

-- 파싱된 usage 레코드 캐시
CREATE TABLE usage_records (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_name  TEXT    NOT NULL,
    model       TEXT    NOT NULL,
    date_key    TEXT    NOT NULL,  -- "YYYY-MM-DD" 또는 "unknown"
    tokens      INTEGER NOT NULL,
    cost        REAL    NOT NULL DEFAULT 0.0,
    hour        INTEGER,           -- 0-23, 로컬 시간 기준 (NULL 가능)
    dow         INTEGER            -- 0=월 ~ 6=일 (NULL 가능)
);
```

## /api/stats 응답 형태

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

쿼리 파라미터: `?start=YYYY-MM-DD&end=YYYY-MM-DD` (둘 다 생략 시 전체 기간)

## JSONL 레코드 예시

파서가 처리하는 주요 포맷:

```jsonl
{"type":"assistant","timestamp":"2026-02-17T14:00:00.000Z","model":"claude-sonnet-4-5","costUsd":0.012,"message":{"usage":{"input_tokens":1000,"output_tokens":500,"cache_read_input_tokens":2000,"cache_creation_input_tokens":0}}}
{"type":"result","timestamp":"2026-02-17T14:01:00.000Z","model":"claude-sonnet-4-5","costUsd":0.005,"usage":{"input_tokens":500,"output_tokens":200}}
```

`total_tokens` = `input_tokens + output_tokens + cache_read_input_tokens + cache_creation_input_tokens`  
`usage` 필드는 레코드 최상위 또는 `message.usage` 안에 있을 수 있음

## 주의사항

- `index.html` 수정 시 `go build`를 다시 해야 바이너리에 반영됨 (`//go:embed`)  
  개발 중 빠른 반복이 필요하면 `go run .` 사용 — 매번 재컴파일하지만 빌드 파일 불필요
- SQLite 스키마 변경 시 `ensureSchema`의 마이그레이션 로직 확인 필요 (현재는 hour/dow 컬럼 누락 시 테이블 drop & rebuild)
- DB 초기화 필요 시: `rm usage_cache.db` 후 재실행하면 자동으로 전체 재파싱
- 테스트 파일 없음 — 테스트 추가 시 `*_test.go` 파일 생성
