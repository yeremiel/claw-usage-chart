package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncTruncateReplacesFileRecordsWithoutDuplication(t *testing.T) {
	tmp := t.TempDir()
	agentsDir := filepath.Join(tmp, "agents")
	sessionDir := filepath.Join(agentsDir, "alpha", "sessions")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}

	fileA := filepath.Join(sessionDir, "a.jsonl")
	fileB := filepath.Join(sessionDir, "b.jsonl")
	writeSessionTokens(t, fileA, []int{10, 20})
	writeSessionTokens(t, fileB, []int{5})

	dbPath := filepath.Join(tmp, "usage_cache.db")
	db, err := openDB(dbPath)
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer db.Close()

	if _, err := Sync(db, agentsDir); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	assertUsageTotals(t, db, 3, 35)

	// Truncate/rewrite fileA with different content.
	writeSessionTokens(t, fileA, []int{7})
	if _, err := Sync(db, agentsDir); err != nil {
		t.Fatalf("sync after truncate: %v", err)
	}
	assertUsageTotals(t, db, 2, 12)

	var fileARows int
	if err := db.QueryRow("SELECT COUNT(*) FROM usage_records WHERE source_file = ?", fileA).Scan(&fileARows); err != nil {
		t.Fatalf("count rows for fileA: %v", err)
	}
	if fileARows != 1 {
		t.Fatalf("expected 1 row for truncated file, got %d", fileARows)
	}

	if _, err := Sync(db, agentsDir); err != nil {
		t.Fatalf("third sync: %v", err)
	}
	assertUsageTotals(t, db, 2, 12)
}

func assertUsageTotals(t *testing.T, db *sql.DB, wantCount, wantTokens int) {
	t.Helper()

	var gotCount, gotTokens int
	if err := db.QueryRow(
		"SELECT COUNT(*), COALESCE(SUM(tokens), 0) FROM usage_records",
	).Scan(&gotCount, &gotTokens); err != nil {
		t.Fatalf("query totals: %v", err)
	}
	if gotCount != wantCount || gotTokens != wantTokens {
		t.Fatalf("totals mismatch: got count=%d tokens=%d, want count=%d tokens=%d",
			gotCount, gotTokens, wantCount, wantTokens)
	}
}

func writeSessionTokens(t *testing.T, path string, tokens []int) {
	t.Helper()

	var b strings.Builder
	for i, tok := range tokens {
		ts := time.Date(2026, 2, 17, 0, i, 0, 0, time.UTC).Format(time.RFC3339)
		fmt.Fprintf(&b, `{"timestamp":"%s","model":"test-model","usage":{"input_tokens":%d,"output_tokens":0}}`+"\n", ts, tok)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
