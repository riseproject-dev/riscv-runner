package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

// demandMatch iterates pending jobs FIFO, groups by k8s_pool, and provisions
// runners until demand is met or pool capacity runs out. Capacity is fetched
// once per pool per iteration and decremented locally (invariants 4232868,
// 40476b8).
func (a *App) demandMatch(ctx context.Context) error {
	pending, err := a.DB.GetPendingJobs(ctx)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		slog.Debug("No pending jobs to process")
		return nil
	}

	ids := make([]string, 0, len(pending))
	for _, j := range pending {
		ids = append(ids, i64s(j.JobID))
	}
	slog.Info("Processing pending jobs", "count", len(pending), "job_ids", ids)

	byPool := map[string][]internal.Job{}
	for _, j := range pending {
		byPool[j.K8sPool] = append(byPool[j.K8sPool], j)
	}

	for pool, jobs := range byPool {
		cap, err := a.K8s.AvailableSlots(ctx, pool)
		if err != nil {
			slog.Error("AvailableSlots failed", "k8s_pool", pool, "err", err)
			continue
		}
		slog.Info("Capacity for k8s_pool",
			"k8s_pool", pool, "total", cap.Total, "active", cap.Active, "available", cap.Available)
		slots := cap.Available
		if slots <= 0 {
			continue
		}
		for _, j := range jobs {
			if slots <= 0 {
				slog.Debug("Capacity for k8s_pool is now 0", "k8s_pool", pool)
				break
			}
			if a.tryProvision(ctx, j) {
				slots--
			}
		}
	}
	return nil
}

// tryProvision evaluates demand/cap/availability for one job and provisions
// a runner if all checks pass. Returns true when a worker row was created
// (caller decrements pool slots regardless of provisioning success — the row
// occupies a slot until the scheduler marks it failed/orphaned).
func (a *App) tryProvision(ctx context.Context, j internal.Job) bool {
	e := j.Entity()
	labels := parseLabels(j.JobLabels)
	if _, err := internal.ParseEntityType(j.EntityType); err != nil {
		slog.Error("invalid entity_type on job", "entity", e, "job_id", j.JobID, "err", err)
		return false
	}

	jobCount, workerCount, err := a.DB.GetPoolDemand(ctx, j.EntityID, labels)
	if err != nil {
		slog.Error("GetPoolDemand failed", "entity", e, "err", err)
		return false
	}
	if jobCount <= workerCount {
		slog.Info("Demand met for entity",
			"entity", e, "labels", labels, "jobs_count", jobCount, "workers_count", workerCount)
		return false
	}

	cfg, ok := internal.EntityConfigs[j.EntityID]
	maxWorkers := internal.DefaultMaxWorkers
	if ok && cfg.MaxWorkers != nil {
		maxWorkers = *cfg.MaxWorkers
	}
	if !ok || cfg.MaxWorkers != nil {
		count, err := a.DB.GetTotalWorkersForEntity(ctx, j.EntityID)
		if err != nil {
			slog.Error("GetTotalWorkersForEntity failed", "entity", e, "err", err)
			return false
		}
		if count >= maxWorkers {
			slog.Info("Max workers allocated for entity",
				"entity", e, "labels", labels, "workers_count", count, "max_workers", maxWorkers)
			return false
		}
	}

	runnerName, err := a.reserveRunnerName(ctx, j, labels)
	if err != nil {
		slog.Error("Failed to generate unique runner name",
			"entity", e, "k8s_pool", j.K8sPool, "err", err)
		return false
	}

	if err := a.provisionRunner(ctx, j, runnerName, labels); err != nil {
		slog.Error("Failed to provision runner",
			"entity", e, "runner_name", runnerName, "k8s_pool", j.K8sPool, "err", err)
		info := internal.FailureInfoV2{Reason: internal.ReasonPodAllocationFailure}
		_ = a.DB.MarkWorkerFailed(ctx, runnerName, "", info, nil)
		// Row was created, slot is consumed.
		return true
	}

	slog.Info("Provisioned runner",
		"entity", e, "runner_name", runnerName, "k8s_pool", j.K8sPool)
	return true
}

// reserveRunnerName picks a random name and persists it before the pod is
// created. workers.pod_name is the table's PK, so the INSERT doubles as a
// "is this name still free?" check across schedulers — avoiding name reuse
// is also what keeps k8s pod and GitHub runner registrations unambiguous.
// We retry on duplicate until we find a fresh suffix.
func (a *App) reserveRunnerName(ctx context.Context, j internal.Job, labels []string) (string, error) {
	e := j.Entity()
	repoFullName := j.RepoFullName
	var repoPtr *string
	if e.Type == internal.EntityUser {
		repoPtr = &repoFullName
	}
	for i := 0; i < 5; i++ {
		suffix := randSuffix(9)
		candidate := a.Config.RunnerPrefix + suffix
		w := internal.Worker{
			PodName:        candidate,
			Provider:       j.Provider,
			EntityID:       j.EntityID,
			EntityName:     j.EntityName,
			EntityType:     j.EntityType,
			InstallationID: j.InstallationID,
			RepoFullName:   repoPtr,
			K8sPool:        j.K8sPool,
			K8sImage:       j.K8sImage,
		}
		err := a.DB.AddWorker(ctx, w, labels)
		if err == nil {
			return candidate, nil
		}
		if errors.Is(err, internal.ErrDuplicatePodName) {
			slog.Warn("Runner name collision, regenerating", "entity", e, "runner_name", candidate)
			continue
		}
		return "", err
	}
	return "", fmt.Errorf("exhausted retries")
}

// provisionRunner: auth → JIT config → k8s create. Org runners share one
// group (RUNNER_GROUP_NAME); user runners use the repo-default group (id=1).
func (a *App) provisionRunner(ctx context.Context, j internal.Job, runnerName string, labels []string) error {
	e := j.Entity()
	token, err := a.ghAuthenticate(ctx, j.InstallationID, e, authCtx{
		RepoFullName: j.RepoFullName,
	})
	if err != nil {
		return err
	}
	var jitConfig string
	switch e.Type {
	case internal.EntityOrganization:
		gid, err := a.GH.EnsureRunnerGroup(ctx, token, j.EntityName, a.Config.RunnerGroup)
		if err != nil {
			slog.Error("EnsureRunnerGroup failed", "entity", e, "group_name", a.Config.RunnerGroup, "err", err)
			return err
		}
		slog.Debug("Runner group ready", "entity", e, "group_name", a.Config.RunnerGroup, "group_id", gid)
		jitConfig, err = a.GH.CreateJITRunnerConfigOrg(ctx, token, j.EntityName, runnerName, gid, labels)
		if err != nil {
			slog.Error("CreateJITRunnerConfig failed", "entity", e, "runner_name", runnerName, "err", err)
			return err
		}
	case internal.EntityUser:
		jitConfig, err = a.GH.CreateJITRunnerConfigRepo(ctx, token, j.RepoFullName, runnerName, labels)
		if err != nil {
			slog.Error("CreateJITRunnerConfig failed", "entity", e, "runner_name", runnerName, "err", err)
			return err
		}
	default:
		panic("unhandled EntityType: " + string(e.Type))
	}
	slog.Debug("Created JIT runner config", "entity", e, "runner_name", runnerName)
	return a.K8s.ProvisionRunner(ctx, jitConfig, runnerName, j.K8sImage, j.K8sPool, e)
}

func parseLabels(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// randSuffix returns a cryptographically-random [a-z0-9] string of length n.
func randSuffix(n int) string {
	const suffixAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	out := make([]byte, n)
	max := big.NewInt(int64(len(suffixAlphabet)))
	for i := range out {
		x, _ := rand.Int(rand.Reader, max)
		out[i] = suffixAlphabet[x.Int64()]
	}
	return string(out)
}
