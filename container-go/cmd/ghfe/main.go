// Command ghfe runs the GitHub webhook frontend.
// See container-go/CONTRACT.md for the full HTTP and webhook surface.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/riseproject-dev/riscv-runner-app/container-go/internal"
)

func main() {
	cfg, err := internal.LoadConfigFromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, "init: "+err.Error())
		os.Exit(2)
	}

	internal.InitSlog(cfg.LogLevel, os.Stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := internal.OpenDB(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "init db: "+err.Error())
		os.Exit(2)
	}
	defer db.Close()

	gh, err := internal.NewGHClient(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "init gh: "+err.Error())
		os.Exit(2)
	}

	app := &App{Config: cfg, DB: db, GH: gh}
	srv := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", internal.HTTPPort),
		Handler:           app.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		shutdownCtx, sCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer sCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	slog.Info("Starting server", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("server error", "err", err)
		os.Exit(1)
	}
}

// App holds the ghfe runtime dependencies handed to each request handler.
type App struct {
	Config internal.Config
	DB     internal.DB
	GH     internal.GitHubClient
}

func (a *App) Routes() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", a.handleHealth)
	mux.HandleFunc("POST /", a.withPerfLog(a.handleWebhook))
	mux.HandleFunc("GET /setup/org", a.handleSetupOrg)
	mux.HandleFunc("GET /setup/personal", a.handleSetupPersonal)
	mux.HandleFunc("GET /trace/entity/{entity_id}", a.handleTraceEntity)
	mux.HandleFunc("GET /trace/installation/{installation_id}", a.handleTraceInstallation)
	mux.HandleFunc("GET /trace/job/{job_id}", a.handleTraceJob)
	mux.HandleFunc("GET /trace/payload/{event_id}", a.handleTracePayload)
	return mux
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok"))
}

// perfLogger captures the response status so withPerfLog can include it in
// the access log, and exposes a flag handlers flip true to opt in.
type perfLogger struct {
	http.ResponseWriter
	status int
	print  bool
}

func (a *perfLogger) WriteHeader(code int) { a.status = code; a.ResponseWriter.WriteHeader(code) }
func (a *perfLogger) Write(b []byte) (int, error) {
	if a.status == 0 {
		a.status = 200
	}
	return a.ResponseWriter.Write(b)
}

type perfLoggerCtxKey struct{}

// withPerfLog emits one access-log line per request, but only when the
// handler explicitly opts in via enablePerfLog. Discards health checks
// to keep the log signal-to-noise high.
func (a *App) withPerfLog(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		al := &perfLogger{ResponseWriter: w}
		start := time.Now()
		ctx := context.WithValue(r.Context(), perfLoggerCtxKey{}, al)
		next(al, r.WithContext(ctx))
		if al.print && !(r.Method == "GET" && r.URL.Path == "/health") {
			slog.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", al.status,
				"elapsed_ms", float64(time.Since(start).Microseconds())/1000.0,
			)
		}
	}
}

func enablePerfLog(r *http.Request) {
	if al, ok := r.Context().Value(perfLoggerCtxKey{}).(*perfLogger); ok {
		al.print = true
	}
}

func httpError(w http.ResponseWriter, status int, msg string) {
	switch {
	case status == 200:
		slog.Debug(msg)
	case status >= 500:
		slog.Error(msg, "status", status)
	default:
		slog.Warn(msg, "status", status)
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(msg))
}
