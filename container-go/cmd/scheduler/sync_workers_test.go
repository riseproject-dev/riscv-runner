package main

import (
	"context"
	"testing"
	"time"

	"github.com/riseproject-dev/riscv-runner-app/container-go/internal"
)

// pendingWorker builds a Worker + matching Pod ready for the phase-3 tests.
func pendingWorker(name string, runningAt *time.Time) internal.Worker {
	return internal.Worker{
		PodName: name, Provider: "github",
		EntityID: 1, EntityName: "e", EntityType: "Organization",
		InstallationID: 9, K8sPool: "scw-em-rv1", K8sImage: "img",
		Status: "running", RunningAt: runningAt,
	}
}

func runningPod(name string) internal.Pod {
	now := time.Now().UTC()
	return internal.Pod{
		Name: name, Phase: "Running", CreationTime: now.Add(-30 * time.Minute),
		Containers: []internal.ContainerStatus{{Name: "runner", Running: true, RunningStarted: &now}},
	}
}

// TestPhase3_OfflineRunnerPastTimeoutFails covers b9c25e0: a GH runner in
// "offline" status past RUNNER_REGISTRATION_TIMEOUT_SECONDS gets killed.
func TestPhase3_OfflineRunnerPastTimeoutFails(t *testing.T) {
	app, db, gh, kube := schedTestApp()
	stale := time.Now().Add(-2 * internal.RunnerRegistrationTimeout)

	w := pendingWorker("rise-riscv-runner-staging-abc", &stale)
	pod := runningPod(w.PodName)
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "running"
	kube.PodsByName[w.PodName] = pod

	gh.OnListRunnersOrgGroup = func(_, _ string, _ int64) ([]internal.GHRunner, error) {
		return []internal.GHRunner{{ID: 1, Name: w.PodName, Status: "offline", Busy: false}}, nil
	}

	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatalf("syncWorkersState: %v", err)
	}
	if len(db.MarkFailed) != 1 {
		t.Fatalf("expected MarkWorkerFailed, got %v", db.MarkFailed)
	}
	if db.MarkFailed[0].Info.Reason != internal.ReasonRunnerNeverRegistered {
		t.Errorf("reason=%q want runner_never_registered", db.MarkFailed[0].Info.Reason)
	}
	if len(kube.KillCalls) != 1 || kube.KillCalls[0] != w.PodName {
		t.Errorf("expected KillPod for %s, got %v", w.PodName, kube.KillCalls)
	}
}

// TestPhase3_OnlineIdleRunnerPastTimeoutFails covers 83469ab: a runner
// idle past RUNNER_PENDING_TIMEOUT_SECONDS yields a runner_idle failure.
func TestPhase3_OnlineIdleRunnerPastTimeoutFails(t *testing.T) {
	app, db, gh, kube := schedTestApp()
	stale := time.Now().Add(-2 * internal.RunnerPendingTimeout)

	w := pendingWorker("rise-riscv-runner-staging-xyz", &stale)
	pod := runningPod(w.PodName)
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "running"
	kube.PodsByName[w.PodName] = pod

	gh.OnListRunnersOrgGroup = func(_, _ string, _ int64) ([]internal.GHRunner, error) {
		return []internal.GHRunner{{ID: 2, Name: w.PodName, Status: "online", Busy: false}}, nil
	}

	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatalf("syncWorkersState: %v", err)
	}
	if len(db.MarkFailed) != 1 || db.MarkFailed[0].Info.Reason != internal.ReasonRunnerIdle {
		t.Fatalf("expected RunnerIdle failure, got %v", db.MarkFailed)
	}
}

// TestSyncWorkersState_PhasesIsolated covers be1434c: an orphan in phase 1
// produces a `completed` worker, then phase 2 still observes the pod-less
// view and does nothing further. (Phases re-fetch their snapshot.)
func TestSyncWorkersState_PhasesIsolated(t *testing.T) {
	app, db, _, _ := schedTestApp()
	w := pendingWorker("rise-riscv-runner-staging-orphan", nil)
	w.Status = "running"
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "running"

	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if len(db.MarkOrphaned) != 1 || db.MarkOrphaned[0] != w.PodName {
		t.Fatalf("expected exactly one MarkWorkerOrphaned, got %v", db.MarkOrphaned)
	}
	if len(db.MarkFailed) != 0 {
		t.Errorf("phase 1 should not produce a MarkFailed call: %v", db.MarkFailed)
	}
}
