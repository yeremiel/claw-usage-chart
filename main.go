package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

//go:embed index.html favicon.svg
var staticFiles embed.FS

func main() {
	cfg := ParseFlags()

	// ── 경로 설정 ────────────────────────────────────────────────────────────
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("홈 디렉터리 확인 불가: %v", err)
	}
	agentsDir := getEnv("OCL_AGENTS_DIR", filepath.Join(home, ".openclaw", "agents"))

	var defaultDBPath string
	exe, err := os.Executable()
	if err == nil {
		defaultDBPath = filepath.Join(filepath.Dir(exe), "usage_cache.db")
	} else {
		defaultDBPath = "usage_cache.db"
	}
	dbPath := getEnv("OCL_DB_PATH", defaultDBPath)

	// ── 시작 전 액션 ─────────────────────────────────────────────────────────
	if cfg.Reset {
		resetDBCache(dbPath)
	}

	// ── 데몬 fork (부모 경로) ────────────────────────────────────────────────
	if cfg.Daemon && !isDaemonChild() {
		forkDaemon()
		if cfg.Open {
			openBrowser(fmt.Sprintf("http://%s:%s", browserHost(cfg.Host), cfg.Port))
		}
		os.Exit(0)
	}

	// ── SQLite 열기 ──────────────────────────────────────────────────────────
	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("DB 열기 실패 %s: %v", dbPath, err)
	}
	defer db.Close()

	// ── 라우트 ───────────────────────────────────────────────────────────────
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

	// ── Graceful shutdown ────────────────────────────────────────────────────
	addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
	srv := &http.Server{
		Addr:    addr,
		Handler: loggingMiddleware(mux),
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	daemon := isDaemonChild()

	go func() {
		<-ctx.Done()
		log.Println("종료 시그널 수신, 서버 종료 중...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("서버 종료 오류: %v", err)
		}
		if daemon {
			removePIDFile()
		}
	}()

	// ── 서버 시작 ────────────────────────────────────────────────────────────
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("포트 바인딩 실패 %s: %v", addr, err)
	}

	// 리스너가 성공한 후에만 PID 파일 작성 (포트 충돌 시 stale PID 방지)
	if daemon {
		if err := writePIDFile(); err != nil {
			log.Fatalf("PID 파일 쓰기 실패: %v", err)
		}
	}

	fmt.Printf("Claw Usage Chart → http://localhost:%s\n", cfg.Port)
	fmt.Printf("  Agents dir : %s\n", agentsDir)
	fmt.Printf("  DB cache   : %s\n", dbPath)

	if cfg.Open && !daemon {
		openBrowser(fmt.Sprintf("http://%s:%s", browserHost(cfg.Host), cfg.Port))
	}

	if err := srv.Serve(ln); err != http.ErrServerClosed {
		log.Fatalf("서버 오류: %v", err)
	}
	log.Println("서버 정상 종료")
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
