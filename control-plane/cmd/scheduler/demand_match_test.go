// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"errors"
	"testing"

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

// TestDemandMatch_SkipsWhenSlotsNonPositive locks invariant 40476b8: skip on
// available_slots <= 0, never go negative.
func TestDemandMatch_SkipsWhenSlotsNonPositive(t *testing.T) {
	app, db, _, kube := schedTestApp()
	db.Jobs = []internal.Job{{
		JobID: 1, Status: "pending", Provider: "github",
		EntityID: 1, EntityName: "e", EntityType: "Organization",
		RepoFullName: "e/r", InstallationID: 9,
		K8sPool: "scw-em-rv1", K8sImage: "img",
	}}
	db.SetPoolDemand(1, nil, 5, 0) // demand > supply

	kube.SlotsByPool["scw-em-rv1"] = 0
	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if len(kube.ProvisionCalls) != 0 {
		t.Fatalf("expected no provisioning, got %v", kube.ProvisionCalls)
	}

	kube.SlotsByPool["scw-em-rv1"] = -2
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
			K8sPool: "scw-em-rv1", K8sImage: "img",
		})
	}
	db.SetPoolDemand(1, nil, 3, 0)
	kube.SlotsByPool["scw-em-rv1"] = 5

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	calls := kube.SlotCalls["scw-em-rv1"]
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
		K8sPool: "scw-em-rv1", K8sImage: "img",
	}}
	db.SetPoolDemand(1, nil, 1, 0)
	kube.SlotsByPool["scw-em-rv1"] = 1
	kube.OnProvisionRunner = func(_, _, _, _ string, _ internal.Entity) error {
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
		K8sPool: "scw-em-rv1", K8sImage: "img",
	}}
	db.SetPoolDemand(internal.PyTorchOrgID, nil, 1, 0)
	db.EntityWorkerCnt[internal.PyTorchOrgID] = 20 // at cap
	kube.SlotsByPool["scw-em-rv1"] = 5

	if err := app.demandMatch(context.Background()); err != nil {
		t.Fatalf("demandMatch: %v", err)
	}
	if len(kube.ProvisionCalls) != 0 {
		t.Fatalf("expected no provisioning at cap, got %v", kube.ProvisionCalls)
	}
}
