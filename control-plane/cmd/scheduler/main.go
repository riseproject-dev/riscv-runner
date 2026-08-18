// SPDX-License-Identifier: MIT

// Command scheduler runs the reconciliation loop + the read-only HTTP
// dashboards (/usage, /history, /jobs, /workers).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
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

	k, err := internal.NewK8sClient(cfg.K8sKubeYAML)
	if err != nil {
		fmt.Fprintln(os.Stderr, "init k8s: "+err.Error())
		os.Exit(2)
	}

	app := &App{Config: cfg, DB: db, GH: gh, K8s: k}

	srv := &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", internal.HTTPPort),
		Handler:           app.Routes(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		slog.Info("Starting server", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
		}
	}()

	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
		<-sigs
		cancel()
		shutdownCtx, sCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer sCancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	for {
		if err := ctx.Err(); err != nil {
			return
		}
		runSchedulerIteration(ctx, app)
		if err := db.WaitForJob(ctx, internal.PollInterval); err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Debug("wait_for_job error", "err", err)
		}
	}
}

// runSchedulerIteration runs one tick. Panics are recovered so a single
// bad iteration doesn't bring the process down.
func runSchedulerIteration(ctx context.Context, a *App) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Scheduler error", "panic", r, "stack", string(debug.Stack()))
		}
	}()
	err := a.DB.WithWorkerLock(ctx, func(ctx context.Context) error {
		if err := a.syncJobsState(ctx); err != nil {
			slog.Error("sync_jobs_state error", "err", err)
		}
		if err := a.syncWorkersState(ctx); err != nil {
			slog.Error("sync_workers_state error", "err", err)
		}
		if err := a.demandMatch(ctx); err != nil {
			slog.Error("demand_match error", "err", err)
		}
		return nil
	})
	if err != nil {
		slog.Error("Scheduler error", "err", err)
	}
}

// App carries scheduler runtime dependencies.
type App struct {
	Config internal.Config
	DB     internal.DB
	GH     internal.GitHubClient
	K8s    internal.KubeClient
}
