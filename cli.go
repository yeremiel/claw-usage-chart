package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"syscall"
)

// version은 빌드 시 ldflags로 주입 가능 (-X main.version=...)
var version = "dev"

const pidFilePath = "/tmp/claw-usage-chart.pid"
const daemonEnvKey = "__CLAW_DAEMON_CHILD"

// Config는 CLI 플래그 + 환경변수에서 병합된 설정을 담는다.
type Config struct {
	Host string
	Port string

	Daemon  bool
	Stop    bool
	Status  bool
	Open    bool
	Reset   bool
	Version bool
}

// ParseFlags는 CLI 플래그를 파싱하고 환경변수와 병합한다 (플래그 우선).
// --version, --status, --stop은 즉시 처리 후 os.Exit(0).
func ParseFlags() Config {
	var cfg Config

	flag.StringVar(&cfg.Port, "port", "", "서버 포트 (기본: 8585, 환경변수: OCL_PORT)")
	flag.StringVar(&cfg.Port, "p", "", "서버 포트 (--port 축약)")
	flag.StringVar(&cfg.Host, "host", "", "바인드 주소 (기본: 0.0.0.0, 환경변수: OCL_HOST)")
	flag.BoolVar(&cfg.Daemon, "daemon", false, "백그라운드 데몬으로 실행")
	flag.BoolVar(&cfg.Daemon, "d", false, "백그라운드 데몬으로 실행 (--daemon 축약)")
	flag.BoolVar(&cfg.Stop, "stop", false, "실행 중인 데몬 종료")
	flag.BoolVar(&cfg.Status, "status", false, "데몬 실행 상태 확인")
	flag.BoolVar(&cfg.Open, "open", false, "서버 시작 후 브라우저 열기")
	flag.BoolVar(&cfg.Open, "o", false, "서버 시작 후 브라우저 열기 (--open 축약)")
	flag.BoolVar(&cfg.Reset, "reset", false, "시작 전 SQLite 캐시 삭제")
	flag.BoolVar(&cfg.Version, "version", false, "버전 출력 후 종료")
	flag.BoolVar(&cfg.Version, "v", false, "버전 출력 후 종료 (--version 축약)")

	flag.Parse()

	if cfg.Version {
		fmt.Printf("claw-usage-chart %s\n", version)
		os.Exit(0)
	}

	if cfg.Status {
		checkDaemonStatus()
		os.Exit(0)
	}

	if cfg.Stop {
		stopDaemon()
		os.Exit(0)
	}

	// 환경변수와 병합 (플래그가 비어있으면 환경변수 사용)
	if cfg.Host == "" {
		cfg.Host = getEnv("OCL_HOST", "0.0.0.0")
	}
	if cfg.Port == "" {
		cfg.Port = getEnv("OCL_PORT", "8585")
	}

	return cfg
}

// ── PID 파일 관리 ──────────────────────────────────────────────────────────

func writePIDFile() error {
	return os.WriteFile(pidFilePath, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

func removePIDFile() {
	os.Remove(pidFilePath)
}

func readPID() int {
	data, err := os.ReadFile(pidFilePath)
	if err != nil {
		return 0
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return pid
}

func isProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Unix에서 FindProcess는 항상 성공. signal 0으로 실제 생존 확인.
	return proc.Signal(syscall.Signal(0)) == nil
}

// ── 데몬 관리 ──────────────────────────────────────────────────────────────

func checkDaemonStatus() {
	pid := readPID()
	if pid > 0 && isProcessRunning(pid) {
		fmt.Printf("claw-usage-chart 데몬 실행 중 (PID %d)\n", pid)
	} else {
		fmt.Println("claw-usage-chart 데몬이 실행 중이 아닙니다")
		if pid > 0 {
			removePIDFile()
		}
	}
}

func stopDaemon() {
	pid := readPID()
	if pid <= 0 || !isProcessRunning(pid) {
		fmt.Println("실행 중인 데몬이 없습니다")
		removePIDFile()
		return
	}
	if !isOwnProcess(pid) {
		fmt.Printf("PID %d 은 claw-usage-chart 프로세스가 아닙니다 (stale PID 파일 삭제)\n", pid)
		removePIDFile()
		return
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		log.Fatalf("프로세스 찾기 실패 (PID %d): %v", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		log.Fatalf("SIGTERM 전송 실패 (PID %d): %v", pid, err)
	}
	fmt.Printf("claw-usage-chart 데몬에 종료 신호 전송 (PID %d)\n", pid)
}

// isOwnProcess는 해당 PID의 프로세스가 claw-usage-chart 바이너리인지 확인한다.
func isOwnProcess(pid int) bool {
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err == nil {
		return strings.Contains(exe, "claw-usage-chart")
	}
	// macOS: /proc 없으므로 ps로 확인
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err != nil {
		return false
	}
	return strings.Contains(strings.TrimSpace(string(out)), "claw-usage-chart")
}

func isDaemonChild() bool {
	return os.Getenv(daemonEnvKey) == "1"
}

// forkDaemon은 현재 바이너리를 백그라운드 자식 프로세스로 재실행한다.
// 자식은 __CLAW_DAEMON_CHILD=1 환경변수로 데몬 모드를 감지한다.
func forkDaemon() {
	exe, err := os.Executable()
	if err != nil {
		log.Fatalf("실행 파일 경로 확인 실패: %v", err)
	}

	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), daemonEnvKey+"=1")
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Stdin = nil

	if err := cmd.Start(); err != nil {
		log.Fatalf("데몬 프로세스 시작 실패: %v", err)
	}

	fmt.Printf("claw-usage-chart 데몬 시작 (PID %d)\n", cmd.Process.Pid)
}

// ── 유틸리티 ───────────────────────────────────────────────────────────────

// browserHost는 브라우저에서 접속할 호스트를 반환한다.
// 와일드카드 바인드(0.0.0.0, ::)인 경우 localhost로 대체.
func browserHost(host string) string {
	if host == "0.0.0.0" || host == "::" || host == "" {
		return "localhost"
	}
	return host
}

func openBrowser(url string) {
	var cmd string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "linux":
		cmd = "xdg-open"
	default:
		return
	}
	exec.Command(cmd, url).Start()
}

// resetDBCache는 SQLite DB 파일과 WAL/SHM 파일을 삭제한다.
func resetDBCache(dbPath string) {
	for _, suffix := range []string{"", "-shm", "-wal"} {
		p := dbPath + suffix
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			log.Printf("캐시 파일 삭제 실패 (%s): %v", p, err)
		}
	}
	fmt.Printf("SQLite 캐시 삭제 완료: %s\n", dbPath)
}
