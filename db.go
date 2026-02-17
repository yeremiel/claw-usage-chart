package main

import (
	"bufio"
	"database/sql"
	"fmt"
	"io"
	"os"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS file_state (
    file_path   TEXT PRIMARY KEY,
    agent_name  TEXT    NOT NULL,
    last_offset INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS usage_records (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    agent_name  TEXT    NOT NULL,
    model       TEXT    NOT NULL,
    date_key    TEXT    NOT NULL,
    tokens      INTEGER NOT NULL,
    cost        REAL    NOT NULL DEFAULT 0.0,
    hour        INTEGER,
    dow         INTEGER
);

CREATE INDEX IF NOT EXISTS idx_rec_agent ON usage_records(agent_name);
CREATE INDEX IF NOT EXISTS idx_rec_model ON usage_records(model);
CREATE INDEX IF NOT EXISTS idx_rec_date  ON usage_records(date_key);
`

// openDB opens (or creates) the SQLite database at dbPath.
func openDB(dbPath string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}
	if _, err := db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureSchema(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// ensureSchema creates the tables and indices if needed.
// If the table is missing the hour/dow columns it drops and rebuilds everything.
func ensureSchema(db *sql.DB) error {
	// Check if hour/dow columns exist
	rows, err := db.Query("PRAGMA table_info(usage_records)")
	if err == nil {
		cols := map[string]bool{}
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, dflt, pk interface{}
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err == nil {
				cols[name] = true
			}
		}
		rows.Close()
		if len(cols) > 0 && (!cols["hour"] || !cols["dow"]) {
			// Drop and rebuild
			if _, err := db.Exec("DROP TABLE IF EXISTS usage_records"); err != nil {
				return err
			}
			if _, err := db.Exec("DROP TABLE IF EXISTS file_state"); err != nil {
				return err
			}
		}
	}
	_, err = db.Exec(schema)
	return err
}

// SyncResult holds statistics from a sync run.
type SyncResult struct {
	NewRecords   int `json:"new_records"`
	SyncedFiles  int `json:"synced_files"`
	SkippedFiles int `json:"skipped_files"`
}

// Sync parses only new bytes from JSONL files and persists them to SQLite.
func Sync(db *sql.DB, agentsDir string) (SyncResult, error) {
	files, err := IterSessionFiles(agentsDir)
	if err != nil {
		return SyncResult{}, err
	}

	var result SyncResult

	tx, err := db.Begin()
	if err != nil {
		return SyncResult{}, err
	}
	defer tx.Rollback()

	insertRec, err := tx.Prepare(`
		INSERT INTO usage_records (agent_name, model, date_key, tokens, cost, hour, dow)
		VALUES (?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return SyncResult{}, err
	}
	defer insertRec.Close()

	for _, sf := range files {
		// Get last offset
		var lastOffset int64
		var hasRow bool
		err := tx.QueryRow(
			"SELECT last_offset FROM file_state WHERE file_path = ?", sf.Path,
		).Scan(&lastOffset)
		if err == nil {
			hasRow = true
		} else if err != sql.ErrNoRows {
			result.SkippedFiles++
			continue
		}

		// Check current size
		fi, err := os.Stat(sf.Path)
		if err != nil {
			result.SkippedFiles++
			continue
		}
		if fi.Size() <= lastOffset {
			result.SkippedFiles++
			continue
		}

		// Read only new bytes
		f, err := os.Open(sf.Path)
		if err != nil {
			result.SkippedFiles++
			continue
		}

		if lastOffset > 0 {
			if _, err := f.Seek(lastOffset, io.SeekStart); err != nil {
				f.Close()
				result.SkippedFiles++
				continue
			}
		}

		var batchCount int
		var newOffset int64 = lastOffset

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 2*1024*1024), 2*1024*1024)
		for scanner.Scan() {
			raw := scanner.Bytes()
			newOffset += int64(len(raw)) + 1 // +1 for newline

			rec := ParseLine(sf.AgentName, raw)
			if rec == nil {
				continue
			}

			var hour, dow interface{}
			if rec.Hour != nil {
				hour = *rec.Hour
			}
			if rec.DOW != nil {
				dow = *rec.DOW
			}

			if _, err := insertRec.Exec(
				rec.AgentName, rec.Model, rec.DateKey,
				rec.Tokens, rec.Cost, hour, dow,
			); err != nil {
				continue
			}
			batchCount++
		}
		f.Close()

		if scanner.Err() != nil {
			result.SkippedFiles++
			continue
		}

		// Update or insert file_state
		if hasRow {
			if _, err := tx.Exec(
				"UPDATE file_state SET last_offset = ? WHERE file_path = ?",
				newOffset, sf.Path,
			); err != nil {
				result.SkippedFiles++
				continue
			}
		} else {
			if _, err := tx.Exec(
				"INSERT INTO file_state (file_path, agent_name, last_offset) VALUES (?, ?, ?)",
				sf.Path, sf.AgentName, newOffset,
			); err != nil {
				result.SkippedFiles++
				continue
			}
		}

		result.NewRecords += batchCount
		result.SyncedFiles++
	}

	if err := tx.Commit(); err != nil {
		return SyncResult{}, err
	}
	return result, nil
}

// ─── aggregation types ────────────────────────────────────────────────────────

type AgentTotal struct {
	Agent   string  `json:"agent"`
	Tokens  int     `json:"tokens"`
	Cost    float64 `json:"cost"`
	Records int     `json:"records"`
}

type ModelTotal struct {
	Model   string  `json:"model"`
	Tokens  int     `json:"tokens"`
	Cost    float64 `json:"cost"`
	Records int     `json:"records"`
}

type DailyTokens struct {
	Date    string  `json:"date"`
	Tokens  int     `json:"tokens"`
	Cost    float64 `json:"cost"`
	Records int     `json:"records"`
}

type HeatmapCell struct {
	DOW    int     `json:"dow"`
	Hour   int     `json:"hour"`
	Tokens int     `json:"tokens"`
	Cost   float64 `json:"cost"`
}

type Summary struct {
	TotalTokens  int     `json:"total_tokens"`
	TotalCost    float64 `json:"total_cost"`
	UsageRecords int     `json:"usage_records"`
	SessionFiles int     `json:"session_files"`
	AgentCount   int     `json:"agent_count"`
	ModelCount   int     `json:"model_count"`
	DayCount     int     `json:"day_count"`
}

type StatsResponse struct {
	GeneratedAt  string        `json:"generated_at"`
	Source       string        `json:"source"`
	Cached       bool          `json:"cached"`
	Sync         SyncResult    `json:"sync"`
	Summary      Summary       `json:"summary"`
	AgentTotals  []AgentTotal  `json:"agent_totals"`
	ModelTotals  []ModelTotal  `json:"model_totals"`
	DailyTokens  []DailyTokens `json:"daily_tokens"`
	Heatmap      []HeatmapCell `json:"heatmap"`
}

// CollectStats syncs then aggregates data from the SQLite cache.
func CollectStats(db *sql.DB, agentsDir, startDate, endDate string) (StatsResponse, error) {
	syncResult, err := Sync(db, agentsDir)
	if err != nil {
		return StatsResponse{}, fmt.Errorf("sync: %w", err)
	}

	// Build date filter SQL
	// "unknown" records always included; dated records filtered by range.
	var dateWhere string
	var dateParams []interface{}

	rangeParts := []string{}
	if startDate != "" {
		rangeParts = append(rangeParts, "date_key >= ?")
		dateParams = append(dateParams, startDate)
	}
	if endDate != "" {
		rangeParts = append(rangeParts, "date_key <= ?")
		dateParams = append(dateParams, endDate)
	}

	if len(rangeParts) > 0 {
		dateWhere = "date_key = 'unknown'"
		for _, p := range rangeParts {
			dateWhere += " OR " + p
		}
		// Wrap in parens for clarity
		dateWhere = "(" + dateWhere + ")"
	} else {
		dateWhere = "1=1" // no filter
	}

	// ── totals ────────────────────────────────────────────────────────────────
	var totalRecords, totalTokens int
	var totalCost float64
	if err := db.QueryRow(
		`SELECT COUNT(*), COALESCE(SUM(tokens),0), COALESCE(SUM(cost),0.0)
		 FROM usage_records WHERE `+dateWhere, dateParams...,
	).Scan(&totalRecords, &totalTokens, &totalCost); err != nil {
		return StatsResponse{}, fmt.Errorf("totals: %w", err)
	}

	// ── session file count ────────────────────────────────────────────────────
	var sessionFiles int
	if err := db.QueryRow("SELECT COUNT(*) FROM file_state").Scan(&sessionFiles); err != nil {
		return StatsResponse{}, fmt.Errorf("file count: %w", err)
	}

	// ── per-agent ─────────────────────────────────────────────────────────────
	rows, err := db.Query(`
		SELECT agent_name, COALESCE(SUM(tokens),0), COUNT(*), COALESCE(SUM(cost),0.0)
		FROM usage_records
		WHERE `+dateWhere+`
		GROUP BY agent_name
		ORDER BY SUM(tokens) DESC`, dateParams...)
	if err != nil {
		return StatsResponse{}, fmt.Errorf("agent totals: %w", err)
	}
	var agentTotals []AgentTotal
	for rows.Next() {
		var a AgentTotal
		if err := rows.Scan(&a.Agent, &a.Tokens, &a.Records, &a.Cost); err == nil {
			a.Cost = roundFloat(a.Cost, 6)
			agentTotals = append(agentTotals, a)
		}
	}
	rows.Close()

	// ── per-model ─────────────────────────────────────────────────────────────
	rows, err = db.Query(`
		SELECT model, COALESCE(SUM(tokens),0), COUNT(*), COALESCE(SUM(cost),0.0)
		FROM usage_records
		WHERE `+dateWhere+`
		GROUP BY model
		ORDER BY SUM(tokens) DESC`, dateParams...)
	if err != nil {
		return StatsResponse{}, fmt.Errorf("model totals: %w", err)
	}
	var modelTotals []ModelTotal
	for rows.Next() {
		var m ModelTotal
		if err := rows.Scan(&m.Model, &m.Tokens, &m.Records, &m.Cost); err == nil {
			m.Cost = roundFloat(m.Cost, 6)
			modelTotals = append(modelTotals, m)
		}
	}
	rows.Close()

	// ── daily series ──────────────────────────────────────────────────────────
	rows, err = db.Query(`
		SELECT date_key, COALESCE(SUM(tokens),0), COUNT(*), COALESCE(SUM(cost),0.0)
		FROM usage_records
		WHERE `+dateWhere+`
		GROUP BY date_key
		ORDER BY
		    CASE WHEN date_key = 'unknown' THEN 1 ELSE 0 END,
		    date_key`, dateParams...)
	if err != nil {
		return StatsResponse{}, fmt.Errorf("daily: %w", err)
	}
	var daily []DailyTokens
	for rows.Next() {
		var d DailyTokens
		if err := rows.Scan(&d.Date, &d.Tokens, &d.Records, &d.Cost); err == nil {
			d.Cost = roundFloat(d.Cost, 6)
			daily = append(daily, d)
		}
	}
	rows.Close()

	// ── heatmap ───────────────────────────────────────────────────────────────
	heatWhere := "hour IS NOT NULL AND dow IS NOT NULL AND (" + dateWhere + ")"
	rows, err = db.Query(`
		SELECT dow, hour, COALESCE(SUM(tokens),0), COALESCE(SUM(cost),0.0)
		FROM usage_records
		WHERE `+heatWhere+`
		GROUP BY dow, hour
		ORDER BY dow, hour`, dateParams...)
	if err != nil {
		return StatsResponse{}, fmt.Errorf("heatmap: %w", err)
	}
	var heatmap []HeatmapCell
	for rows.Next() {
		var h HeatmapCell
		if err := rows.Scan(&h.DOW, &h.Hour, &h.Tokens, &h.Cost); err == nil {
			h.Cost = roundFloat(h.Cost, 6)
			heatmap = append(heatmap, h)
		}
	}
	rows.Close()

	// Ensure slices are never nil (JSON [] not null)
	if agentTotals == nil {
		agentTotals = []AgentTotal{}
	}
	if modelTotals == nil {
		modelTotals = []ModelTotal{}
	}
	if daily == nil {
		daily = []DailyTokens{}
	}
	if heatmap == nil {
		heatmap = []HeatmapCell{}
	}

	return StatsResponse{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Source:      agentsDir,
		Cached:      true,
		Sync:        syncResult,
		Summary: Summary{
			TotalTokens:  totalTokens,
			TotalCost:    roundFloat(totalCost, 6),
			UsageRecords: totalRecords,
			SessionFiles: sessionFiles,
			AgentCount:   len(agentTotals),
			ModelCount:   len(modelTotals),
			DayCount:     len(daily),
		},
		AgentTotals: agentTotals,
		ModelTotals: modelTotals,
		DailyTokens: daily,
		Heatmap:     heatmap,
	}, nil
}

func roundFloat(f float64, precision int) float64 {
	// Simple rounding to N decimal places
	p := 1.0
	for i := 0; i < precision; i++ {
		p *= 10
	}
	return float64(int(f*p+0.5)) / p
}
