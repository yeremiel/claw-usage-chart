package main

import (
	"database/sql"
	"encoding/json"
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

//go:embed index.html favicon.svg
var staticFiles embed.FS

func main() {
	// ── Environment config ────────────────────────────────────────────────────
	host := getEnv("OCL_HOST", "0.0.0.0")
	port := getEnv("OCL_PORT", "8585")

	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("cannot determine home dir: %v", err)
	}
	agentsDir := getEnv("OCL_AGENTS_DIR", filepath.Join(home, ".openclaw", "agents"))

	// DB path defaults to the directory containing the binary
	var defaultDBPath string
	exe, err := os.Executable()
	if err == nil {
		defaultDBPath = filepath.Join(filepath.Dir(exe), "usage_cache.db")
	} else {
		defaultDBPath = "usage_cache.db"
	}
	dbPath := getEnv("OCL_DB_PATH", defaultDBPath)

	// ── Open SQLite ───────────────────────────────────────────────────────────
	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("open db %s: %v", dbPath, err)
	}
	defer db.Close()

	// ── Routes ────────────────────────────────────────────────────────────────
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		content, err := staticFiles.ReadFile("index.html")
		if err != nil {
			http.Error(w, "index.html not found", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(content)
	})

	mux.HandleFunc("/favicon.svg", func(w http.ResponseWriter, r *http.Request) {
		content, err := staticFiles.ReadFile("favicon.svg")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Write(content)
	})

	mux.HandleFunc("/api/stats", statsHandler(db, agentsDir))

	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	})

	// ── Start server ──────────────────────────────────────────────────────────
	addr := fmt.Sprintf("%s:%s", host, port)
	fmt.Printf("Claw Usage Chart → http://localhost:%s\n", port)
	fmt.Printf("  Agents dir : %s\n", agentsDir)
	fmt.Printf("  DB cache   : %s\n", dbPath)

	if err := http.ListenAndServe(addr, loggingMiddleware(mux)); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func statsHandler(db *sql.DB, agentsDir string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		start := q.Get("start")
		end := q.Get("end")

		stats, err := CollectStats(db, agentsDir, start, end)

		var payload []byte
		var status int
		if err != nil {
			payload, _ = json.Marshal(map[string]string{"error": err.Error()})
			status = http.StatusInternalServerError
		} else {
			payload, err = json.Marshal(stats)
			if err != nil {
				payload, _ = json.Marshal(map[string]string{"error": err.Error()})
				status = http.StatusInternalServerError
			} else {
				status = http.StatusOK
			}
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(status)
		w.Write(payload)
	}
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[usage-dashboard] %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
