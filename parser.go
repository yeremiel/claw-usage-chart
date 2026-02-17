package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// rawRecord is a flexible struct for deserialising JSONL lines.
// Fields can appear at top level or inside "message".
type rawRecord struct {
	Type      interface{}     `json:"type"`
	Timestamp interface{}     `json:"timestamp"`
	Message   json.RawMessage `json:"message"`
	Usage     json.RawMessage `json:"usage"`
	CostUsd   *float64        `json:"costUsd"`
	Model     string          `json:"model"`
	ModelID   string          `json:"modelId"`
	ModelID2  string          `json:"model_id"`
}

type rawMessage struct {
	Usage   json.RawMessage `json:"usage"`
	Model   string          `json:"model"`
	ModelID string          `json:"modelId"`
	ModelID2 string         `json:"model_id"`
}

type rawUsage struct {
	// Explicit total fields
	TotalTokens  interface{} `json:"totalTokens"`
	TotalTokens2 interface{} `json:"total_tokens"`
	Total        interface{} `json:"total"`
	Tokens       interface{} `json:"tokens"`

	// Individual token fields
	Input                    interface{} `json:"input"`
	Output                   interface{} `json:"output"`
	CacheRead                interface{} `json:"cacheRead"`
	CacheWrite               interface{} `json:"cacheWrite"`
	InputTokens              interface{} `json:"input_tokens"`
	OutputTokens             interface{} `json:"output_tokens"`
	CacheReadInputTokens     interface{} `json:"cache_read_input_tokens"`
	CacheCreationInputTokens interface{} `json:"cache_creation_input_tokens"`
	ReasoningTokens          interface{} `json:"reasoning_tokens"`

	// Cost sub-field
	Cost interface{} `json:"cost"`
}

// UsageRecord is what we store after parsing one JSONL line.
type UsageRecord struct {
	AgentName string
	Model     string
	DateKey   string // "YYYY-MM-DD" or "unknown"
	Tokens    int
	Cost      float64
	Hour      *int // 0-23, nil if unknown
	DOW       *int // 0=Mon..6=Sun, nil if unknown
}

// SessionFile pairs an agent name with a JSONL file path.
type SessionFile struct {
	AgentName string
	Path      string
}

// IterSessionFiles returns all *.jsonl files under agentsDir/<agent>/sessions/*.
func IterSessionFiles(agentsDir string) ([]SessionFile, error) {
	entries, err := os.ReadDir(agentsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var results []SessionFile
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sessDir := filepath.Join(agentsDir, e.Name(), "sessions")
		files, err := filepath.Glob(filepath.Join(sessDir, "*.jsonl"))
		if err != nil {
			continue
		}
		sort.Strings(files)
		for _, f := range files {
			results = append(results, SessionFile{AgentName: e.Name(), Path: f})
		}
	}
	return results, nil
}

// ParseLine parses a single JSONL line into a UsageRecord.
// Returns nil if the line should be skipped.
func ParseLine(agentName string, line []byte) *UsageRecord {
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 {
		return nil
	}

	var rec rawRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return nil
	}

	// Extract usage dict
	usage := extractUsage(&rec)
	if usage == nil {
		return nil
	}

	tokens := extractTotalTokens(usage)
	if tokens <= 0 {
		return nil
	}

	ts := extractTimestamp(&rec)
	dateKey := parseTimestampToDate(ts)
	if dateKey == "" {
		dateKey = "unknown"
	}

	cost := extractCost(&rec, usage)
	model := extractModel(&rec)
	hour, dow := parseTimestampToHourDOW(ts)

	return &UsageRecord{
		AgentName: agentName,
		Model:     model,
		DateKey:   dateKey,
		Tokens:    tokens,
		Cost:      cost,
		Hour:      hour,
		DOW:       dow,
	}
}

// ── internal helpers ─────────────────────────────────────────────────────────

func extractUsage(rec *rawRecord) *rawUsage {
	// Try message.usage first
	if len(rec.Message) > 0 {
		var msg rawMessage
		if err := json.Unmarshal(rec.Message, &msg); err == nil && len(msg.Usage) > 0 {
			var u rawUsage
			if err := json.Unmarshal(msg.Usage, &u); err == nil {
				return &u
			}
		}
	}
	// Then top-level usage
	if len(rec.Usage) > 0 {
		var u rawUsage
		if err := json.Unmarshal(rec.Usage, &u); err == nil {
			return &u
		}
	}
	return nil
}

func toInt(v interface{}) int {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		return int(val)
	case int:
		return val
	case int64:
		return int(val)
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return 0
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return int(f)
	case bool:
		if val {
			return 1
		}
		return 0
	}
	return 0
}

func extractTotalTokens(u *rawUsage) int {
	// Check explicit total fields first
	for _, v := range []interface{}{u.TotalTokens, u.TotalTokens2, u.Total, u.Tokens} {
		if n := toInt(v); n > 0 {
			return n
		}
	}
	// Sum individual fields
	sum := 0
	for _, v := range []interface{}{
		u.Input, u.Output, u.CacheRead, u.CacheWrite,
		u.InputTokens, u.OutputTokens,
		u.CacheReadInputTokens, u.CacheCreationInputTokens,
		u.ReasoningTokens,
	} {
		if n := toInt(v); n > 0 {
			sum += n
		}
	}
	return sum
}

func extractModel(rec *rawRecord) string {
	// Try message fields first
	if len(rec.Message) > 0 {
		var msg rawMessage
		if err := json.Unmarshal(rec.Message, &msg); err == nil {
			for _, s := range []string{msg.Model, msg.ModelID, msg.ModelID2} {
				if s = strings.TrimSpace(s); s != "" {
					return s
				}
			}
		}
	}
	// Then top-level
	for _, s := range []string{rec.Model, rec.ModelID, rec.ModelID2} {
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return "unknown"
}

func extractCost(rec *rawRecord, u *rawUsage) float64 {
	// Prefer top-level costUsd (OpenClaw native field)
	if rec.CostUsd != nil {
		return *rec.CostUsd
	}
	// Fall back to usage.cost
	if u.Cost == nil {
		return 0
	}
	switch v := u.Cost.(type) {
	case float64:
		return v
	case map[string]interface{}:
		if total, ok := v["total"]; ok {
			if f, ok := total.(float64); ok {
				return f
			}
		}
	}
	return 0
}

func extractTimestamp(rec *rawRecord) interface{} {
	if rec.Timestamp != nil {
		return rec.Timestamp
	}
	if len(rec.Message) > 0 {
		var m map[string]interface{}
		if err := json.Unmarshal(rec.Message, &m); err == nil {
			if ts, ok := m["timestamp"]; ok {
				return ts
			}
		}
	}
	return nil
}

func parseTimestampToDate(ts interface{}) string {
	t := parseTimestampToTime(ts)
	if t.IsZero() {
		return ""
	}
	// Use local time for date
	return t.Local().Format("2006-01-02")
}

func parseTimestampToHourDOW(ts interface{}) (*int, *int) {
	t := parseTimestampToTime(ts)
	if t.IsZero() {
		return nil, nil
	}
	lt := t.Local()
	h := lt.Hour()
	// weekday: Go Sunday=0, Monday=1 … Saturday=6
	// Python: 0=Mon..6=Sun → convert
	wd := int(lt.Weekday())
	dow := (wd + 6) % 7 // Sun(0)→6, Mon(1)→0, …, Sat(6)→5
	return &h, &dow
}

// parseTimestampToTime converts a raw timestamp value to time.Time (UTC).
func parseTimestampToTime(ts interface{}) time.Time {
	if ts == nil {
		return time.Time{}
	}
	switch v := ts.(type) {
	case float64:
		sec := v
		if v > 10_000_000_000 {
			sec = v / 1000.0
		}
		return time.Unix(int64(sec), 0).UTC()
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return time.Time{}
		}
		// Numeric string
		if isNumeric(s) {
			f, err := strconv.ParseFloat(s, 64)
			if err != nil {
				return time.Time{}
			}
			if len(s) >= 13 {
				f /= 1000.0
			}
			return time.Unix(int64(f), 0).UTC()
		}
		// ISO 8601 — replace Z with +00:00
		iso := strings.Replace(s, "Z", "+00:00", 1)
		t, err := time.Parse(time.RFC3339Nano, iso)
		if err == nil {
			return t.UTC()
		}
		// Try without nanoseconds
		t, err = time.Parse(time.RFC3339, iso)
		if err == nil {
			return t.UTC()
		}
		// Bare date
		if len(s) >= 10 && s[4] == '-' && s[7] == '-' {
			t, err = time.Parse("2006-01-02", s[:10])
			if err == nil {
				return t.UTC()
			}
		}
	}
	return time.Time{}
}

func isNumeric(s string) bool {
	s = strings.TrimPrefix(s, "-")
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(s) > 0
}
