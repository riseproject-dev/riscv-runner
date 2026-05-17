// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

// syncWorkersState runs the 5 reconciliation phases. The whole call sits
// inside WithWorkerLock (see runSchedulerIteration), so all writes share a
// transaction and execute under LOCK TABLE workers IN EXCLUSIVE MODE.
func (a *App) syncWorkersState(ctx context.Context) error {
	pods, err := a.K8s.ListPods(ctx)
	if err != nil {
		return err
	}
	podsByName := make(map[string]internal.Pod, len(pods))
	for _, p := range pods {
		podsByName[p.Name] = p
	}

	ghRunnersByKey := map[orgRunnerKey]map[string]internal.GHRunner{}

	workers, err := a.DB.GetWorkersForReconcile(ctx, time.Hour)
	if err != nil {
		return err
	}
	workersByName := indexWorkers(workers)

	a.OrphanSweep(ctx, podsByName, workersByName)
	a.PodPhaseSync(ctx, podsByName, workersByName)

	workers, err = a.DB.GetWorkersForReconcile(ctx, time.Hour)
	if err != nil {
		return err
	}
	workersByName = indexWorkers(workers)

	a.HealthChecks(ctx, podsByName, workersByName, ghRunnersByKey)

	workers, err = a.DB.GetWorkersForReconcile(ctx, time.Hour)
	if err != nil {
		return err
	}
	workersByName = indexWorkers(workers)

	a.GitHubCleanup(ctx, workersByName, ghRunnersByKey)
	a.DeleteTerminalPods(ctx, podsByName)
	return nil
}

func indexWorkers(ws []internal.Worker) map[string]internal.Worker {
	out := make(map[string]internal.Worker, len(ws))
	for _, w := range ws {
		out[w.PodName] = w
	}
	return out
}

// Phase 1: workers without a matching pod → orphaned.
func (a *App) OrphanSweep(ctx context.Context, pods map[string]internal.Pod, workers map[string]internal.Worker) {
	for name, w := range workers {
		if _, ok := pods[name]; !ok && (w.Status == "pending" || w.Status == "running") {
			if err := a.DB.MarkWorkerOrphaned(ctx, name); err != nil {
				slog.Error("MarkWorkerOrphaned failed", "entity", w.Entity(), "pod_name", name, "err", err)
			}
		}
	}
}

// Phase 2: K8s pod phase → DB status.
func (a *App) PodPhaseSync(ctx context.Context, pods map[string]internal.Pod, workers map[string]internal.Worker) {
	for name, p := range pods {
		w, ok := workers[name]
		if !ok {
			continue
		}
		switch p.Phase {
		case "Running":
			if w.Status == "pending" {
				_ = a.DB.MarkWorkerRunning(ctx, name, p.NodeName, p.RunnerStartedAt())
			}
		case "Succeeded":
			if w.Status == "pending" || w.Status == "running" {
				_ = a.DB.MarkWorkerCompleted(ctx, name, p.NodeName, p.FinishedAt())
			}
		case "Failed":
			if w.Status == "pending" || w.Status == "running" {
				info := a.K8s.CollectPodFailureInfo(ctx, p, internal.ReasonPodFailed)
				if err := a.DB.MarkWorkerFailed(ctx, name, p.NodeName, info, p.FinishedAt()); err != nil {
					slog.Error("MarkWorkerFailed failed", "entity", w.Entity(), "pod_name", name, "err", err)
				}
			}
		}
	}
}

// Phase 3: per-GitHub-target health check loop. Builds gh_runners cache for Phase 4 as a side effect.
func (a *App) HealthChecks(ctx context.Context, pods map[string]internal.Pod, workers map[string]internal.Worker, cache map[orgRunnerKey]map[string]internal.GHRunner) {
	groups := map[orgRunnerKey][]internal.Worker{}
	for _, w := range workers {
		if w.Status != "pending" && w.Status != "running" {
			continue
		}
		k := runnerKeyForWorker(w)
		groups[k] = append(groups[k], w)
	}

	for key, ws := range groups {
		token, err := a.ghAuthenticate(ctx, key.InstallationID, key.Entity(), authCtx{
			RepoFullName: key.RepoFullName,
		})
		if err != nil {
			slog.Error("Failed to authenticate for installation",
				"entity", key.Entity(),
				"installation_id", key.InstallationID, "gh_runner_target", key.Target(), "err", err)
			continue
		}
		ghRunners := a.fetchGHRunners(ctx, key, token, cache)

		for _, w := range ws {
			pod, ok := pods[w.PodName]
			if !ok {
				// Phase 1 should have caught this; defensive skip.
				continue
			}
			ghRunner, ghOK := ghRunners[w.PodName]
			a.classifyWorker(ctx, w, pod, token, key, ghRunner, ghOK)
		}
	}
}

// fetchGHRunners memoises the GitHub-side runner list per scope so Phase 4
// can reuse it without a second API call.
func (a *App) fetchGHRunners(ctx context.Context, key orgRunnerKey, token string, cache map[orgRunnerKey]map[string]internal.GHRunner) map[string]internal.GHRunner {
	if r, ok := cache[key]; ok {
		return r
	}
	out := map[string]internal.GHRunner{}
	switch key.EntityType {
	case internal.EntityOrganization:
		gid, err := a.GH.EnsureRunnerGroup(ctx, token, key.EntityName, a.Config.RunnerGroup)
		if err != nil {
			slog.Error("Failed to list GH runners",
				"entity", key.Entity(), "group_name", a.Config.RunnerGroup, "target", key.EntityName, "err", err)
			cache[key] = out
			return out
		}
		list, err := a.GH.ListRunnersOrgGroup(ctx, token, key.EntityName, gid)
		if err != nil {
			slog.Error("Failed to list GH runners",
				"entity", key.Entity(), "group_name", a.Config.RunnerGroup, "group_id", gid, "target", key.EntityName, "err", err)
			cache[key] = out
			return out
		}
		for _, r := range list {
			out[r.Name] = r
		}
	case internal.EntityUser:
		list, err := a.GH.ListRunnersRepo(ctx, token, key.RepoFullName)
		if err != nil {
			slog.Error("Failed to list GH runners", "entity", key.Entity(), "target", key.RepoFullName, "err", err)
			cache[key] = out
			return out
		}
		for _, r := range list {
			if strings.HasPrefix(r.Name, a.Config.RunnerPrefix) {
				out[r.Name] = r
			}
		}
	default:
		panic("unhandled EntityType: " + string(key.EntityType))
	}
	cache[key] = out
	return out
}

// classifyWorker walks the per-worker decision tree. Each branch either
// skips, logs, or calls failAndCleanup with a FailureReason.
func (a *App) classifyWorker(ctx context.Context, w internal.Worker, pod internal.Pod, token string, key orgRunnerKey, gh internal.GHRunner, hasRunner bool) {
	var runnerStatus string
	var runnerBusy bool
	if hasRunner {
		runnerStatus = gh.Status
		runnerBusy = gh.Busy
	}

	creation := pod.CreationTime
	pendingAge := internal.AgeSeconds(&creation)
	runningAt := w.RunningAt
	runningAge := internal.AgeSeconds(runningAt)
	e := w.Entity()

	switch {
	case w.Status == "pending":
		if pendingAge < internal.PodPendingTimeout.Seconds() {
			slog.Debug("Worker is still pending",
				"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus)
			return
		}
		slog.Warn("Worker is still pending past timeout, marking as failed",
			"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus,
			"timeout_seconds", int(internal.PodPendingTimeout.Seconds()))
		a.failAndCleanup(ctx, w, pod, token, key, gh, hasRunner, internal.ReasonPodStuckPending)

	case w.Status == "running" && !hasRunner:
		exists, _ := a.DB.JobExistsForPod(ctx, w.PodName)
		if exists {
			slog.Debug("Worker has run a job and self-unregistered, skipping",
				"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus)
			return
		}
		if runningAge < internal.RunnerRegistrationTimeout.Seconds() {
			slog.Info("Worker not known github runner and may still register",
				"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus)
			return
		}
		slog.Warn("Worker not known github runner and failed to register",
			"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus,
			"timeout_seconds", int(internal.RunnerRegistrationTimeout.Seconds()))
		a.failAndCleanup(ctx, w, pod, token, key, gh, hasRunner, internal.ReasonRunnerNeverRegistered)

	case w.Status == "running" && runnerStatus == "offline":
		if runningAge < internal.RunnerRegistrationTimeout.Seconds() {
			slog.Info("Worker is known github runner and may still register",
				"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus)
			return
		}
		slog.Warn("Worker known github runner failed to register, marking as failed",
			"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus,
			"timeout_seconds", int(internal.RunnerRegistrationTimeout.Seconds()))
		a.failAndCleanup(ctx, w, pod, token, key, gh, hasRunner, internal.ReasonRunnerNeverRegistered)

	case w.Status == "running" && runnerStatus == "online" && !runnerBusy:
		if runningAge < internal.RunnerPendingTimeout.Seconds() {
			slog.Info("Worker is known github runner and may still pick up a job",
				"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus)
			return
		}
		slog.Warn("Worker known github runner idle past timeout, marking as failed",
			"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus,
			"timeout_seconds", int(internal.RunnerPendingTimeout.Seconds()))
		a.failAndCleanup(ctx, w, pod, token, key, gh, hasRunner, internal.ReasonRunnerIdle)

	case w.Status == "running" && runnerStatus == "online" && runnerBusy:
		// Healthy; nothing to do.

	case w.Status == "running":
		slog.Info("Worker has unknown github status",
			"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus)
		if runningAge < internal.RunnerRegistrationTimeout.Seconds() {
			return
		}
		slog.Warn("Worker known github runner in unknown state, marking as failed",
			"entity", e, "worker", w.PodName, "worker_status", w.Status, "runner_status", runnerStatus,
			"timeout_seconds", int(internal.RunnerRegistrationTimeout.Seconds()))
		a.failAndCleanup(ctx, w, pod, token, key, gh, hasRunner, internal.ReasonRunnerNeverRegistered)

	default:
		slog.Error("unexpected worker status",
			"entity", e, "worker", w.PodName, "worker_status", w.Status,
			"runner_status", runnerStatus, "runner_busy", runnerBusy)
	}
}

// failAndCleanup marks a worker failed, kills its pod, and removes any stale
// GitHub registration. When GitHub has a runner for the worker we try to
// delete it first: a non-2xx (e.g. 422 "runner is busy") is GitHub's signal
// that a job is actually executing — abort cleanup so we don't kill a worker
// that's doing useful work we missed signal for. Otherwise collect
// diagnostics, mark failed, and kill the pod; Phase 5's grace window will
// later remove the Failed pod.
func (a *App) failAndCleanup(ctx context.Context, w internal.Worker, pod internal.Pod, token string, key orgRunnerKey, gh internal.GHRunner, hasRunner bool, reason internal.FailureReason) {
	e := w.Entity()
	slog.Warn("Health check failed for pod", "entity", e, "pod_name", w.PodName, "reason", string(reason))
	if hasRunner {
		if !a.deleteGHRunner(ctx, token, key, gh.ID, w.PodName) {
			slog.Warn("Aborting cleanup: GitHub refused to delete the runner (may be running a job)",
				"entity", e, "worker", w.PodName)
			return
		}
	}
	info := a.K8s.CollectPodFailureInfo(ctx, pod, reason)
	now := time.Now().UTC()
	node := pod.NodeName
	if node == "" && w.K8sNode != nil {
		node = *w.K8sNode
	}
	if err := a.DB.MarkWorkerFailed(ctx, w.PodName, node, info, &now); err != nil {
		slog.Error("MarkWorkerFailed failed", "entity", e, "pod_name", w.PodName, "err", err)
	}
	if err := a.K8s.KillPod(ctx, pod.Name); err != nil {
		slog.Error("KillPod failed", "entity", e, "pod_name", pod.Name, "err", err)
		return
	}
	slog.Info("Killed runner pod (activeDeadlineSeconds=1)", "entity", e, "pod_name", pod.Name)
}

// deleteGHRunner returns true on 204/404 success. Logs and returns false on error.
func (a *App) deleteGHRunner(ctx context.Context, token string, key orgRunnerKey, runnerID int64, workerName string) bool {
	var err error
	switch key.EntityType {
	case internal.EntityOrganization:
		err = a.GH.DeleteRunnerOrg(ctx, token, key.EntityName, runnerID)
	case internal.EntityUser:
		err = a.GH.DeleteRunnerRepo(ctx, token, key.RepoFullName, runnerID)
	default:
		panic("unhandled EntityType: " + string(key.EntityType))
	}
	if err != nil {
		slog.Error("Failed to delete GH runner",
			"entity", key.Entity(), "name", workerName, "id", runnerID, "target", key.Target(), "err", err)
		return false
	}
	slog.Info("Deleted GH runner",
		"entity", key.Entity(), "name", workerName, "id", runnerID, "target", key.Target())
	return true
}

// Phase 4: delete any GitHub runner whose worker is terminal/missing.
func (a *App) GitHubCleanup(ctx context.Context, workers map[string]internal.Worker, cache map[orgRunnerKey]map[string]internal.GHRunner) {
	for key, ghRunners := range cache {
		token, err := a.ghAuthenticate(ctx, key.InstallationID, key.Entity(), authCtx{
			RepoFullName: key.RepoFullName,
		})
		if err != nil {
			slog.Error("Failed to authenticate for installation",
				"entity", key.Entity(),
				"installation_id", key.InstallationID, "gh_runner_target", key.Target(), "err", err)
			continue
		}
		for name, r := range ghRunners {
			if !strings.HasPrefix(name, a.Config.RunnerPrefix) {
				continue
			}
			w, known := workers[name]
			if known && (w.Status == "completed" || w.Status == "failed") {
				slog.Info("Runner has matching completed worker", "entity", key.Entity(), "runner", name)
				_ = a.deleteGHRunner(ctx, token, key, r.ID, name)
				continue
			}
			if !known {
				slog.Info("Runner is unknown", "entity", key.Entity(), "runner", name)
				_ = a.deleteGHRunner(ctx, token, key, r.ID, name)
			}
		}
	}
}

// Phase 5: delete terminal pods past the grace window.
func (a *App) DeleteTerminalPods(ctx context.Context, pods map[string]internal.Pod) {
	now := time.Now().UTC()
	for name, p := range pods {
		if p.Phase != "Succeeded" && p.Phase != "Failed" {
			continue
		}
		finished := p.FinishedAt()
		if finished == nil {
			t := p.CreationTime
			finished = &t
		}
		if finished == nil || now.Sub(*finished) < internal.PodDeleteGrace {
			continue
		}
		if err := a.K8s.DeletePod(ctx, name); err != nil {
			slog.Error("Failed to delete pod", "pod_name", name, "err", err)
			continue
		}
		slog.Info("Deleted runner pod", "pod_name", name)
	}
}
