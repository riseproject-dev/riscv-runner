// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
	"github.com/riseproject-dev/riscv-runner/control-plane/internal/testutil"
)

// schedTestApp wires a scheduler App with fakes shared across tests.
func schedTestApp() (*App, *testutil.FakeDB, *testutil.FakeGH, *testutil.FakeKube) {
	db := testutil.NewFakeDB()
	gh := &testutil.FakeGH{}
	kube := testutil.NewFakeKube()
	app := &App{
		Config: internal.Config{Prod: false, RunnerPrefix: "rise-riscv-runner-staging-", RunnerGroup: "RISE RISC-V Runners (staging)"},
		DB:     db, GH: gh, K8s: kube,
	}
	return app, db, gh, kube
}

var scwSel = internal.NodeSelector{Board: internal.BoardScalewayEMRV1, Provider: internal.ProviderScaleway}

var scwKey = scwSel.Key()

func TestFairJobQueue_Empty(t *testing.T) {
	queue := newFairJobQueue(nil)
	if queue.len != 0 || len(queue.byEntity) != 0 {
		t.Fatalf("got %+v, want empty queue", queue)
	}
}

func TestRoundRobinByEntity(t *testing.T) {
	jobs := []internal.Job{
		{JobID: 1, EntityID: 1},
		{JobID: 2, EntityID: 2},
		{JobID: 3, EntityID: 2},
		{JobID: 4, EntityID: 2},
		{JobID: 5, EntityID: 2},
		{JobID: 6, EntityID: 2},
		{JobID: 7, EntityID: 3},
		{JobID: 8, EntityID: 3},
	}

	ordered := drainFairJobQueue(jobs, nil)
	want := []int64{1, 2, 7, 3, 8, 4, 5, 6}
	for i, jobID := range want {
		if ordered[i].JobID != jobID {
			t.Fatalf("job at position %d = %d, want %d", i, ordered[i].JobID, jobID)
		}
	}
}

func TestRoundRobinByEntity_ReordersEntitiesEachRound(t *testing.T) {
	t0 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	at := func(n int) time.Time { return t0.Add(time.Duration(n) * time.Minute) }
	jobs := []internal.Job{
		{JobID: 1, EntityID: 3, CreatedAt: at(0)},
		{JobID: 2, EntityID: 2, CreatedAt: at(1)},
		{JobID: 3, EntityID: 2, CreatedAt: at(2)},
		{JobID: 4, EntityID: 2, CreatedAt: at(3)},
		{JobID: 5, EntityID: 2, CreatedAt: at(4)},
		{JobID: 6, EntityID: 1, CreatedAt: at(5)},
		{JobID: 7, EntityID: 3, CreatedAt: at(5)},
		{JobID: 8, EntityID: 2, CreatedAt: at(6)},
	}

	ordered := drainFairJobQueue(jobs, nil)
	want := []int64{1, 2, 6, 3, 7, 4, 5, 8}
	for i, jobID := range want {
		if ordered[i].JobID != jobID {
			t.Fatalf("job at position %d = %d, want %d", i, ordered[i].JobID, jobID)
		}
	}
}

func TestFairJobQueue_EqualTimestampsUseJobID(t *testing.T) {
	timestamp := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []internal.Job{
		{JobID: 20, EntityID: 2, CreatedAt: timestamp},
		{JobID: 10, EntityID: 1, CreatedAt: timestamp},
	}

	ordered := drainFairJobQueue(jobs, nil)
	if ordered[0].JobID != 10 || ordered[1].JobID != 20 {
		t.Fatalf("got job order [%d %d], want [10 20]", ordered[0].JobID, ordered[1].JobID)
	}
}

func TestFairJobQueue_AccountsForExistingAllocations(t *testing.T) {
	t0 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []internal.Job{
		{JobID: 1, EntityID: 1, CreatedAt: t0},
		{JobID: 2, EntityID: 2, CreatedAt: t0.Add(time.Minute)},
		{JobID: 3, EntityID: 3, CreatedAt: t0.Add(2 * time.Minute)},
	}

	ordered := drainFairJobQueue(jobs, map[int64]int{1: 5, 2: 0, 3: 1})
	want := []int64{2, 3, 1}
	for i, jobID := range want {
		if ordered[i].JobID != jobID {
			t.Fatalf("job at position %d = %d, want %d", i, ordered[i].JobID, jobID)
		}
	}
}

func TestFairJobQueue_LeastAllocatedEntityCatchesUp(t *testing.T) {
	t0 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	jobs := []internal.Job{{JobID: 1, EntityID: 1, CreatedAt: t0}}
	for i := int64(2); i <= 6; i++ {
		jobs = append(jobs, internal.Job{JobID: i, EntityID: 2, CreatedAt: t0.Add(time.Duration(i) * time.Minute)})
	}

	ordered := drainFairJobQueue(jobs, map[int64]int{1: 5})
	wantEntities := []int64{2, 2, 2, 2, 2, 1}
	for i, entityID := range wantEntities {
		if ordered[i].EntityID != entityID {
			t.Fatalf("entity at position %d = %d, want %d", i, ordered[i].EntityID, entityID)
		}
	}
}

func drainFairJobQueue(jobs []internal.Job, active map[int64]int) []internal.Job {
	if active == nil {
		active = make(map[int64]int)
	}
	queue := newFairJobQueue(jobs)
	ordered := make([]internal.Job, 0, len(jobs))
	for queue.len > 0 {
		job := queue.pop(active)
		ordered = append(ordered, job)
		active[job.EntityID]++
	}
	return ordered
}

func TestDemandMatch_LimitedCapacityPrefersLeastAllocatedEntity(t *testing.T) {
	app, db, _, kube := schedTestApp()
	t0 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	db.Jobs = []internal.Job{
		{
			JobID: 1, Status: "pending", Provider: "github", CreatedAt: t0,
			EntityID: 1, EntityName: "orga", EntityType: "Organization",
			RepoFullName: "orga/r", InstallationID: 9, K8sSelector: scwSel, K8sImage: "img",
		},
		{
			JobID: 2, Status: "pending", Provider: "github", CreatedAt: t0.Add(time.Minute),
			EntityID: 2, EntityName: "orgb", EntityType: "Organization",
			RepoFullName: "orgb/r", InstallationID: 10, K8sSelector: scwSel, K8sImage: "img",
		},
	}
	db.SetPoolDemand(1, nil, 1, 0)
	db.SetPoolDemand(2, nil, 1, 0)
	kube.SlotsByPool[scwKey] = 1
	kube.ActiveByEntityByPool[scwKey] = map[int64]int{1: 1}
	var provisioned internal.Entity
	kube.OnProvisionRunner = func(_, _, _ string, _ internal.NodeSelector, entity internal.Entity) error {
		provisioned = entity
		return nil
	}

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if provisioned.ID != 2 {
		t.Fatalf("provisioned entity %d, want 2", provisioned.ID)
	}
}

func TestDemandMatch_FailedProvisionUpdatesLocalAllocation(t *testing.T) {
	app, db, _, kube := schedTestApp()
	t0 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	db.Jobs = []internal.Job{
		{
			JobID: 1, Status: "pending", Provider: "github", CreatedAt: t0,
			EntityID: 1, EntityName: "orga", EntityType: "Organization",
			RepoFullName: "orga/r", InstallationID: 1, K8sSelector: scwSel, K8sImage: "img",
		},
		{
			JobID: 2, Status: "pending", Provider: "github", CreatedAt: t0.Add(time.Minute),
			EntityID: 1, EntityName: "orga", EntityType: "Organization",
			RepoFullName: "orga/r", InstallationID: 1, K8sSelector: scwSel, K8sImage: "img",
		},
		{
			JobID: 3, Status: "pending", Provider: "github", CreatedAt: t0.Add(2 * time.Minute),
			EntityID: 2, EntityName: "orgb", EntityType: "Organization",
			RepoFullName: "orgb/r", InstallationID: 2, K8sSelector: scwSel, K8sImage: "img",
		},
	}
	db.SetPoolDemand(1, nil, 2, 0)
	db.SetPoolDemand(2, nil, 1, 0)
	kube.SlotsByPool[scwKey] = 2
	var attempted []int64
	kube.OnProvisionRunner = func(_, _, _ string, _ internal.NodeSelector, entity internal.Entity) error {
		attempted = append(attempted, entity.ID)
		if len(attempted) == 1 {
			return errors.New("boom")
		}
		return nil
	}

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if len(attempted) != 2 || attempted[0] != 1 || attempted[1] != 2 {
		t.Fatalf("attempted entities %v, want [1 2]", attempted)
	}
}

func TestDemandMatch_SatisfiedDemandDoesNotConsumeCapacity(t *testing.T) {
	app, db, _, kube := schedTestApp()
	for i := int64(1); i <= 2; i++ {
		db.Jobs = append(db.Jobs, internal.Job{
			JobID: i, Status: "pending", Provider: "github", EntityID: i,
			EntityName: "org", EntityType: "Organization", RepoFullName: "org/r",
			InstallationID: i, K8sSelector: scwSel, K8sImage: "img",
		})
	}
	db.SetPoolDemand(1, nil, 1, 1)
	db.SetPoolDemand(2, nil, 1, 0)
	kube.SlotsByPool[scwKey] = 1
	var provisioned int64
	kube.OnProvisionRunner = func(_, _, _ string, _ internal.NodeSelector, entity internal.Entity) error {
		provisioned = entity.ID
		return nil
	}

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if provisioned != 2 {
		t.Fatalf("provisioned entity %d, want 2", provisioned)
	}
}

// TestDemandMatch_SkipsWhenSlotsNonPositive locks invariant 40476b8: skip on
// available_slots <= 0, never go negative.
func TestDemandMatch_SkipsWhenSlotsNonPositive(t *testing.T) {
	app, db, _, kube := schedTestApp()
	db.Jobs = []internal.Job{{
		JobID: 1, Status: "pending", Provider: "github",
		EntityID: 1, EntityName: "e", EntityType: "Organization",
		RepoFullName: "e/r", InstallationID: 9,
		K8sSelector: scwSel, K8sImage: "img",
	}}
	db.SetPoolDemand(1, nil, 5, 0) // demand > supply

	kube.SlotsByPool[scwKey] = 0
	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if len(kube.ProvisionCalls) != 0 {
		t.Fatalf("expected no provisioning, got %v", kube.ProvisionCalls)
	}

	kube.SlotsByPool[scwKey] = -2
	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if len(kube.ProvisionCalls) != 0 {
		t.Fatalf("expected no provisioning when slots<0, got %v", kube.ProvisionCalls)
	}
}

// TestDemandMatch_CapacityFetchedOncePerPool locks invariant 4232868.
func TestDemandMatch_CapacityFetchedOncePerPool(t *testing.T) {
	app, db, _, kube := schedTestApp()
	for i := int64(1); i <= 3; i++ {
		db.Jobs = append(db.Jobs, internal.Job{
			JobID: i, Status: "pending", Provider: "github",
			EntityID: 1, EntityName: "e", EntityType: "Organization",
			RepoFullName: "e/r", InstallationID: 9,
			K8sSelector: scwSel, K8sImage: "img",
		})
	}
	db.SetPoolDemand(1, nil, 3, 0)
	kube.SlotsByPool[scwKey] = 5

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	calls := kube.SlotCalls[scwKey]
	if calls != 1 {
		t.Fatalf("AvailableSlots called %d times, expected 1", calls)
	}
}

// TestProvisionRunner_FailureMarksWorker locks invariant 9a9d611.
func TestProvisionRunner_FailureMarksWorker(t *testing.T) {
	app, db, _, kube := schedTestApp()
	db.Jobs = []internal.Job{{
		JobID: 1, Status: "pending", Provider: "github",
		EntityID: 1, EntityName: "e", EntityType: "Organization",
		RepoFullName: "e/r", InstallationID: 9,
		K8sSelector: scwSel, K8sImage: "img",
	}}
	db.SetPoolDemand(1, nil, 1, 0)
	kube.SlotsByPool[scwKey] = 1
	kube.OnProvisionRunner = func(_, _, _ string, _ internal.NodeSelector, _ internal.Entity) error {
		return errors.New("boom")
	}

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if len(db.MarkFailed) != 1 {
		t.Fatalf("expected 1 mark_worker_failed call, got %v", db.MarkFailed)
	}
	if db.MarkFailed[0].Info.(internal.FailureInfoV2).Reason != internal.ReasonPodAllocationFailure {
		t.Errorf("failure_info.reason=%q want pod_allocation_failure", db.MarkFailed[0].Info.(internal.FailureInfoV2).Reason)
	}
}

// TestDemandMatch_RespectsEntityMaxWorkers covers the entity cap path.
func TestDemandMatch_RespectsEntityMaxWorkers(t *testing.T) {
	app, db, _, kube := schedTestApp()
	db.Jobs = []internal.Job{{
		JobID: 1, Status: "pending", Provider: "github",
		EntityID: internal.PyTorchOrgID, EntityName: "pytorch", EntityType: "Organization",
		RepoFullName: "pytorch/pytorch", InstallationID: 9,
		K8sSelector: scwSel, K8sImage: "img",
	}}
	db.SetPoolDemand(internal.PyTorchOrgID, nil, 1, 0)
	db.EntityWorkerCnt[internal.PyTorchOrgID] = 20 // at cap
	kube.SlotsByPool[scwKey] = 5

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if len(kube.ProvisionCalls) != 0 {
		t.Fatalf("expected no provisioning at cap, got %v", kube.ProvisionCalls)
	}
}

func TestDemandMatch_EntityAtMaxDoesNotBlockOthers(t *testing.T) {
	app, db, _, kube := schedTestApp()
	db.Jobs = []internal.Job{
		{
			JobID: 1, Status: "pending", Provider: "github",
			EntityID: internal.PyTorchOrgID, EntityName: "pytorch", EntityType: "Organization",
			RepoFullName: "pytorch/pytorch", InstallationID: 1, K8sSelector: scwSel, K8sImage: "img",
		},
		{
			JobID: 2, Status: "pending", Provider: "github",
			EntityID: 2, EntityName: "orgb", EntityType: "Organization",
			RepoFullName: "orgb/r", InstallationID: 2, K8sSelector: scwSel, K8sImage: "img",
		},
	}
	db.SetPoolDemand(internal.PyTorchOrgID, nil, 1, 0)
	db.SetPoolDemand(2, nil, 1, 0)
	db.EntityWorkerCnt[internal.PyTorchOrgID] = 20
	kube.SlotsByPool[scwKey] = 1
	var provisioned int64
	kube.OnProvisionRunner = func(_, _, _ string, _ internal.NodeSelector, entity internal.Entity) error {
		provisioned = entity.ID
		return nil
	}

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if provisioned != 2 {
		t.Fatalf("provisioned entity %d, want 2", provisioned)
	}
}

// TestDemandMatch_RateLimitedEntityBacksOffButOthersProceed locks the incident
// fix: when one entity's token is rate limited, we stop trying to provision for
// that entity this iteration (no repeated 403s) yet still handle other entities
// sharing the pool.
func TestDemandMatch_RateLimitedEntityBacksOffButOthersProceed(t *testing.T) {
	app, db, gh, kube := schedTestApp()
	for i := int64(1); i <= 3; i++ {
		db.Jobs = append(db.Jobs, internal.Job{
			JobID: i, Status: "pending", Provider: "github",
			EntityID: 1, EntityName: "orga", EntityType: "Organization",
			RepoFullName: "orga/r", InstallationID: 9,
			K8sSelector: scwSel, K8sImage: "img",
		})
	}
	db.Jobs = append(db.Jobs, internal.Job{
		JobID: 4, Status: "pending", Provider: "github",
		EntityID: 2, EntityName: "orgb", EntityType: "Organization",
		RepoFullName: "orgb/r", InstallationID: 10,
		K8sSelector: scwSel, K8sImage: "img",
	})
	db.SetPoolDemand(1, nil, 3, 0)
	db.SetPoolDemand(2, nil, 1, 0)
	kube.SlotsByPool[scwKey] = 10

	jitByOrg := map[string]int{}
	gh.OnCreateJITRunnerOrg = func(_, org, _ string, _ int64, _ []string) (string, error) {
		jitByOrg[org]++
		if org == "orga" {
			return "", &internal.GitHubAPIError{StatusCode: 429, Message: "rate limited"}
		}
		return "jit", nil
	}
	provisioned := map[string]int{}
	kube.OnProvisionRunner = func(_, _, _ string, _ internal.NodeSelector, e internal.Entity) error {
		provisioned[e.Name]++
		return nil
	}

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}

	if jitByOrg["orga"] != 1 {
		t.Errorf("rate-limited entity should be tried once, got %d attempts", jitByOrg["orga"])
	}
	if provisioned["orgb"] != 1 {
		t.Errorf("non-limited entity should still be provisioned, got %v", provisioned)
	}
	if provisioned["orga"] != 0 {
		t.Errorf("rate-limited entity should not reach k8s provisioning, got %v", provisioned)
	}
	if len(db.MarkFailed) != 1 || db.MarkFailed[0].Info.(internal.FailureInfoV2).Reason != internal.ReasonRateLimited {
		t.Errorf("expected one rate_limited MarkWorkerFailed, got %v", db.MarkFailed)
	}
}

// TestDemandMatch_GroupsPerProviderOnSameBoard: jobs differing only by
// provider form separate groups, each getting its own capacity lookup and
// budget, and each runner is provisioned with its own selector. Whether those
// budgets are distinct in the cluster is AvailableSlots' business
// (see TestAvailableSlots_MeasuredPerSelector).
func TestDemandMatch_GroupsPerProviderOnSameBoard(t *testing.T) {
	app, db, _, kube := schedTestApp()
	cloudv := internal.NodeSelector{Board: internal.BoardSpacemitK1, Provider: internal.ProviderCloudV10x}
	mengzhuo := internal.NodeSelector{Board: internal.BoardSpacemitK1, Provider: internal.ProviderMengZhuo}

	for i, sel := range []internal.NodeSelector{cloudv, mengzhuo} {
		entityID := int64(i + 1)
		db.Jobs = append(db.Jobs, internal.Job{
			JobID: entityID, Status: "pending", Provider: "github",
			EntityID: entityID, EntityName: "e", EntityType: "Organization",
			RepoFullName: "e/r", InstallationID: 9,
			K8sSelector: sel, K8sImage: "img",
		})
		db.SetPoolDemand(entityID, nil, 1, 0)
	}

	// Only cloudv10x has capacity.
	kube.SlotsByPool[cloudv.Key()] = 1
	kube.SlotsByPool[mengzhuo.Key()] = 0

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if kube.SlotCalls[cloudv.Key()] != 1 || kube.SlotCalls[mengzhuo.Key()] != 1 {
		t.Fatalf("expected one capacity lookup per selector, got cloudv=%d mengzhuo=%d",
			kube.SlotCalls[cloudv.Key()], kube.SlotCalls[mengzhuo.Key()])
	}
	if len(kube.ProvisionCalls) != 1 {
		t.Fatalf("expected only the cloudv10x job provisioned, got %v", kube.ProvisionCalls)
	}
	if got := kube.ProvisionSelectors[kube.ProvisionCalls[0]]; got != cloudv {
		t.Errorf("provisioned with selector %v, want %v", got, cloudv)
	}
}

// TestDemandMatch_FallbackSelectorForLegacyJob: a job row predating
// k8s_selector must still be placed on its board's default provider, never on
// an unconstrained selector.
func TestDemandMatch_FallbackSelectorForLegacyJob(t *testing.T) {
	app, db, _, kube := schedTestApp()
	db.Jobs = []internal.Job{{
		JobID: 1, Status: "pending", Provider: "github",
		EntityID: 1, EntityName: "e", EntityType: "Organization",
		RepoFullName: "e/r", InstallationID: 9,
		K8sSelector: internal.NodeSelector{Board: internal.BoardSpacemitK3, Provider: internal.ProviderISCAS}, K8sImage: "img",
	}}
	db.SetPoolDemand(1, nil, 1, 0)
	want := internal.NodeSelector{Board: internal.BoardSpacemitK3, Provider: internal.ProviderISCAS}
	kube.SlotsByPool[want.Key()] = 1

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if len(kube.ProvisionCalls) != 1 {
		t.Fatalf("expected one provision, got %v", kube.ProvisionCalls)
	}
	if got := kube.ProvisionSelectors[kube.ProvisionCalls[0]]; got != want {
		t.Errorf("provisioned with selector %v, want %v", got, want)
	}
}
