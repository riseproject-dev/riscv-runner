package main

import (
	"context"
	"testing"
	"time"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
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
	if db.MarkFailed[0].Info.(internal.FailureInfoV2).Reason != internal.ReasonRunnerNeverRegistered {
		t.Errorf("reason=%q want runner_never_registered", db.MarkFailed[0].Info.(internal.FailureInfoV2).Reason)
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
	if len(db.MarkFailed) != 1 || db.MarkFailed[0].Info.(internal.FailureInfoV2).Reason != internal.ReasonRunnerIdle {
		t.Fatalf("expected RunnerIdle failure, got %v", db.MarkFailed)
	}
}

// TestPhase2_RunningPhaseTransitionsPendingToRunning covers PodPhaseSync.
func TestPhase2_RunningPhaseTransitionsPendingToRunning(t *testing.T) {
	app, db, _, kube := schedTestApp()
	w := pendingWorker("rise-riscv-runner-staging-p2", nil)
	w.Status = "pending"
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "pending"
	kube.PodsByName[w.PodName] = runningPod(w.PodName)

	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkRunning) != 1 {
		t.Errorf("expected MarkWorkerRunning, got %v", db.MarkRunning)
	}
}

// TestPhase2_SucceededPodMarksWorkerCompleted covers the Succeeded branch.
func TestPhase2_SucceededPodMarksWorkerCompleted(t *testing.T) {
	app, db, _, kube := schedTestApp()
	w := pendingWorker("rise-riscv-runner-staging-done", nil)
	w.Status = "running"
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "running"

	t0 := time.Now()
	pod := internal.Pod{
		Name: w.PodName, Phase: "Succeeded",
		Containers: []internal.ContainerStatus{{Name: "runner", Terminated: true, TerminatedAt: &t0}},
	}
	kube.PodsByName[w.PodName] = pod
	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkComplete) != 1 {
		t.Errorf("expected MarkWorkerCompleted, got %v", db.MarkComplete)
	}
}

// TestPhase2_FailedPodMarksWorkerFailed covers the Failed branch.
func TestPhase2_FailedPodMarksWorkerFailed(t *testing.T) {
	app, db, _, kube := schedTestApp()
	w := pendingWorker("rise-riscv-runner-staging-bad", nil)
	w.Status = "running"
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "running"

	pod := internal.Pod{Name: w.PodName, Phase: "Failed"}
	kube.PodsByName[w.PodName] = pod
	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkFailed) != 1 || db.MarkFailed[0].Info.(internal.FailureInfoV2).Reason != internal.ReasonPodFailed {
		t.Errorf("expected MarkWorkerFailed(pod_failed), got %v", db.MarkFailed)
	}
}

// TestPhase3_PendingPastTimeoutFailsWithStuckPending covers ReasonPodStuckPending.
func TestPhase3_PendingPastTimeoutFailsWithStuckPending(t *testing.T) {
	app, db, _, kube := schedTestApp()
	w := pendingWorker("rise-riscv-runner-staging-pending", nil)
	w.Status = "pending"
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "pending"

	old := time.Now().Add(-2 * internal.PodPendingTimeout)
	pod := internal.Pod{Name: w.PodName, Phase: "Pending", CreationTime: old}
	kube.PodsByName[w.PodName] = pod

	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkFailed) != 1 || db.MarkFailed[0].Info.(internal.FailureInfoV2).Reason != internal.ReasonPodStuckPending {
		t.Errorf("got %v", db.MarkFailed)
	}
}

// TestPhase3_RunningNotGHKnownWithinTimeout is the still-may-register branch.
func TestPhase3_RunningNotGHKnownWithinTimeout(t *testing.T) {
	app, db, gh, kube := schedTestApp()
	recent := time.Now().Add(-10 * time.Second)
	w := pendingWorker("rise-riscv-runner-staging-fresh", &recent)
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "running"
	kube.PodsByName[w.PodName] = runningPod(w.PodName)
	// No GH runners returned
	gh.OnListRunnersOrgGroup = func(_, _ string, _ int64) ([]internal.GHRunner, error) {
		return nil, nil
	}
	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkFailed) != 0 {
		t.Errorf("should not mark failed within registration timeout: %v", db.MarkFailed)
	}
}

// TestPhase3_PodHasRunJobSelfUnregistered: worker has a row in jobs.k8s_pod →
// skip even when GH no longer reports the runner.
func TestPhase3_PodHasRunJobSelfUnregistered(t *testing.T) {
	app, db, gh, kube := schedTestApp()
	stale := time.Now().Add(-2 * internal.RunnerRegistrationTimeout)
	w := pendingWorker("rise-riscv-runner-staging-selfunreg", &stale)
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "running"
	db.JobExistsByPod[w.PodName] = true
	kube.PodsByName[w.PodName] = runningPod(w.PodName)
	gh.OnListRunnersOrgGroup = func(_, _ string, _ int64) ([]internal.GHRunner, error) { return nil, nil }
	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkFailed) != 0 {
		t.Errorf("self-unregistered worker should not be failed: %v", db.MarkFailed)
	}
}

// TestPhase3_OnlineBusyIsHealthy covers the no-op branch.
func TestPhase3_OnlineBusyIsHealthy(t *testing.T) {
	app, db, gh, kube := schedTestApp()
	stale := time.Now().Add(-2 * internal.RunnerPendingTimeout)
	w := pendingWorker("rise-riscv-runner-staging-busy", &stale)
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "running"
	kube.PodsByName[w.PodName] = runningPod(w.PodName)
	gh.OnListRunnersOrgGroup = func(_, _ string, _ int64) ([]internal.GHRunner, error) {
		return []internal.GHRunner{{ID: 1, Name: w.PodName, Status: "online", Busy: true}}, nil
	}
	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkFailed) != 0 {
		t.Errorf("online+busy is healthy: %v", db.MarkFailed)
	}
}

// TestPhase3_UnknownRunnerStatusStillFails covers the catch-all `running` branch.
func TestPhase3_UnknownRunnerStatusStillFails(t *testing.T) {
	app, db, gh, kube := schedTestApp()
	stale := time.Now().Add(-2 * internal.RunnerRegistrationTimeout)
	w := pendingWorker("rise-riscv-runner-staging-unknown", &stale)
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "running"
	kube.PodsByName[w.PodName] = runningPod(w.PodName)
	gh.OnListRunnersOrgGroup = func(_, _ string, _ int64) ([]internal.GHRunner, error) {
		return []internal.GHRunner{{ID: 5, Name: w.PodName, Status: "stuck"}}, nil
	}
	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkFailed) != 1 {
		t.Errorf("unknown status past timeout should fail: %v", db.MarkFailed)
	}
}

// TestPhase4_GitHubCleanup_DeletesUnknownRunners covers the !known branch.
func TestPhase4_GitHubCleanup_DeletesUnknownRunners(t *testing.T) {
	app, db, gh, kube := schedTestApp()
	// One healthy worker we'll leave alone
	w := pendingWorker("rise-riscv-runner-staging-keep", nil)
	w.Status = "completed"
	db.Workers = []internal.Worker{w}
	db.WorkerStatus[w.PodName] = "completed"

	gh.OnListRunnersOrgGroup = func(_, _ string, _ int64) ([]internal.GHRunner, error) {
		return []internal.GHRunner{
			{ID: 1, Name: w.PodName, Status: "offline"},                   // matches completed worker → delete
			{ID: 2, Name: "rise-riscv-runner-staging-ghost", Status: "x"}, // no worker row → delete
			{ID: 3, Name: "unrelated-runner", Status: "x"},                // wrong prefix → skip
		}, nil
	}
	deletes := []int64{}
	gh.OnDeleteRunnerOrg = func(_, _ string, id int64) error {
		deletes = append(deletes, id)
		return nil
	}

	// Force HealthChecks to populate cache: add an active worker to the
	// same scope so HealthChecks visits the key.
	active := pendingWorker("rise-riscv-runner-staging-active", nil)
	active.Status = "running"
	db.Workers = append(db.Workers, active)
	db.WorkerStatus[active.PodName] = "running"
	kube.PodsByName[active.PodName] = runningPod(active.PodName)

	if err := app.syncWorkersState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(deletes) < 2 {
		t.Errorf("expected DeleteRunnerOrg for completed + ghost, got %v", deletes)
	}
}

// TestPhase5_DeleteTerminalPodsPastGrace covers DeleteTerminalPods grace branch.
func TestPhase5_DeleteTerminalPodsPastGrace(t *testing.T) {
	app, _, _, kube := schedTestApp()
	old := time.Now().Add(-2 * internal.PodDeleteGrace)
	kube.PodsByName["stale"] = internal.Pod{
		Name: "stale", Phase: "Succeeded",
		Containers: []internal.ContainerStatus{{Name: "runner", Terminated: true, TerminatedAt: &old}},
	}
	now := time.Now()
	kube.PodsByName["fresh"] = internal.Pod{
		Name: "fresh", Phase: "Succeeded", CreationTime: now,
		Containers: []internal.ContainerStatus{{Name: "runner", Terminated: true, TerminatedAt: &now}},
	}
	// Phase 5 doesn't require workers, so test it directly.
	app.DeleteTerminalPods(context.Background(), kube.PodsByName)
	if len(kube.DeleteCalls) != 1 || kube.DeleteCalls[0] != "stale" {
		t.Errorf("expected only stale deleted, got %v", kube.DeleteCalls)
	}
}

// TestPhase5_SkipsNonTerminalAndFreshTerminal covers the early-continue branches.
func TestPhase5_SkipsNonTerminalAndFreshTerminal(t *testing.T) {
	app, _, _, kube := schedTestApp()
	kube.PodsByName["running"] = internal.Pod{Name: "running", Phase: "Running"}
	kube.PodsByName["pending"] = internal.Pod{Name: "pending", Phase: "Pending"}
	app.DeleteTerminalPods(context.Background(), kube.PodsByName)
	if len(kube.DeleteCalls) != 0 {
		t.Errorf("non-terminal pods should not be deleted: %v", kube.DeleteCalls)
	}
}

// TestFetchGHRunners_UserScopeFiltersPrefix covers the user/repo branch.
func TestFetchGHRunners_UserScopeFiltersPrefix(t *testing.T) {
	app, _, gh, _ := schedTestApp()
	gh.OnListRunnersRepo = func(string, string) ([]internal.GHRunner, error) {
		return []internal.GHRunner{
			{ID: 1, Name: "rise-riscv-runner-staging-good"},
			{ID: 2, Name: "other-runner"},
		}, nil
	}
	cache := map[orgRunnerKey]map[string]internal.GHRunner{}
	key := orgRunnerKey{EntityType: internal.EntityUser, EntityName: "luhenry", RepoFullName: "luhenry/r"}
	got := app.fetchGHRunners(context.Background(), key, "tok", cache)
	if len(got) != 1 {
		t.Errorf("expected prefix filter, got %v", got)
	}
}

// TestFetchGHRunners_Cached avoids the GH call on repeated lookups.
func TestFetchGHRunners_Cached(t *testing.T) {
	app, _, gh, _ := schedTestApp()
	calls := 0
	gh.OnListRunnersOrgGroup = func(_, _ string, _ int64) ([]internal.GHRunner, error) {
		calls++
		return nil, nil
	}
	cache := map[orgRunnerKey]map[string]internal.GHRunner{}
	key := orgRunnerKey{EntityType: internal.EntityOrganization, EntityName: "acme"}
	app.fetchGHRunners(context.Background(), key, "tok", cache)
	app.fetchGHRunners(context.Background(), key, "tok", cache)
	if calls > 1 {
		t.Errorf("expected cache hit, calls=%d", calls)
	}
}

// TestFetchGHRunners_EnsureRunnerGroupError covers the org error path.
func TestFetchGHRunners_EnsureRunnerGroupError(t *testing.T) {
	app, _, gh, _ := schedTestApp()
	gh.OnEnsureRunnerGroup = func(_, _, _ string) (int64, error) { return 0, errBoom }
	cache := map[orgRunnerKey]map[string]internal.GHRunner{}
	got := app.fetchGHRunners(context.Background(), orgRunnerKey{EntityType: internal.EntityOrganization, EntityName: "acme"}, "tok", cache)
	if len(got) != 0 {
		t.Errorf("expected empty on error, got %v", got)
	}
}

// TestFetchGHRunners_ListError covers the list error branch (org).
func TestFetchGHRunners_ListError(t *testing.T) {
	app, _, gh, _ := schedTestApp()
	gh.OnEnsureRunnerGroup = func(_, _, _ string) (int64, error) { return 1, nil }
	gh.OnListRunnersOrgGroup = func(_, _ string, _ int64) ([]internal.GHRunner, error) { return nil, errBoom }
	cache := map[orgRunnerKey]map[string]internal.GHRunner{}
	got := app.fetchGHRunners(context.Background(), orgRunnerKey{EntityType: internal.EntityOrganization, EntityName: "acme"}, "tok", cache)
	if len(got) != 0 {
		t.Errorf("expected empty on list error, got %v", got)
	}
}

// TestDeleteGHRunner_UserScopeUsesRepoDelete confirms repo-scoped delete path.
func TestDeleteGHRunner_UserScopeUsesRepoDelete(t *testing.T) {
	app, _, gh, _ := schedTestApp()
	repoDeleted := false
	gh.OnDeleteRunnerRepo = func(_, repo string, _ int64) error {
		if repo == "luhenry/r" {
			repoDeleted = true
		}
		return nil
	}
	ok := app.deleteGHRunner(context.Background(), "tok",
		orgRunnerKey{EntityType: internal.EntityUser, EntityName: "luhenry", RepoFullName: "luhenry/r"},
		7, "worker-1")
	if !ok || !repoDeleted {
		t.Errorf("expected repo-scoped delete ok=%v called=%v", ok, repoDeleted)
	}
}

// TestDeleteGHRunner_FailureReturnsFalse covers the error path.
func TestDeleteGHRunner_FailureReturnsFalse(t *testing.T) {
	app, _, gh, _ := schedTestApp()
	gh.OnDeleteRunnerOrg = func(_, _ string, _ int64) error { return errBoom }
	ok := app.deleteGHRunner(context.Background(), "tok",
		orgRunnerKey{EntityType: internal.EntityOrganization, EntityName: "acme"}, 1, "w")
	if ok {
		t.Error("expected false on failure")
	}
}

// TestFailAndCleanup_GitHubDeleteFailureAborts covers the "abort cleanup" path.
func TestFailAndCleanup_GitHubDeleteFailureAborts(t *testing.T) {
	app, db, gh, kube := schedTestApp()
	gh.OnDeleteRunnerOrg = func(_, _ string, _ int64) error { return errBoom }
	w := pendingWorker("rise-riscv-runner-staging-busy", nil)
	pod := runningPod(w.PodName)
	kube.PodsByName[w.PodName] = pod
	app.failAndCleanup(context.Background(), w, pod, "tok",
		orgRunnerKey{EntityType: internal.EntityOrganization, EntityName: "e"},
		internal.GHRunner{ID: 1}, true, internal.ReasonRunnerIdle)
	if len(db.MarkFailed) != 0 {
		t.Errorf("MarkFailed should not be called when GH delete aborts: %v", db.MarkFailed)
	}
	if len(kube.KillCalls) != 0 {
		t.Errorf("KillPod should not be called: %v", kube.KillCalls)
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
