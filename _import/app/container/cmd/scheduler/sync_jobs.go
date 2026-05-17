package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

// syncJobsState converges each active job's DB row with GitHub's view of it.
// GitHub can report status="in_progress" with a non-null conclusion — that
// case is treated as completed, since the conclusion is terminal.
func (a *App) syncJobsState(ctx context.Context) error {
	jobs, err := a.DB.GetActiveJobs(ctx)
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if err := a.syncOneJob(ctx, j); err != nil {
			slog.Debug("syncOneJob failed", "entity", j.Entity(), "job_id", j.JobID, "err", err)
		}
	}
	return nil
}

func (a *App) syncOneJob(ctx context.Context, j internal.Job) error {
	if j.RepoFullName == "" {
		return nil
	}
	e := j.Entity()
	if _, err := internal.ParseEntityType(j.EntityType); err != nil {
		return err
	}
	token, err := a.ghAuthenticate(ctx, j.InstallationID, e, authCtx{
		RepoFullName: j.RepoFullName,
		JobID:        j.JobID,
	})
	if err != nil {
		var apiErr *internal.GitHubAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			slog.Warn("Installation not found, marking job failed",
				"entity", e, "installation_id", j.InstallationID, "job_id", j.JobID)
			_, _ = a.DB.MarkJobFailed(ctx, j.JobID, internal.FailureInfoV1{
				Message: fmt.Sprintf("installation not found for installation_id=%d entity_type=%s",
					j.InstallationID, e.Type),
			})
			return nil
		}
		slog.Error("Failed to authenticate for installation",
			"entity", e, "installation_id", j.InstallationID, "err", err)
		return nil
	}

	ghJob, err := a.GH.GetJobInfo(ctx, token, j.RepoFullName, j.JobID)
	if err != nil {
		var apiErr *internal.GitHubAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
			slog.Warn("Job not found, marking as failed", "entity", e, "job_id", j.JobID)
			_, _ = a.DB.MarkJobFailed(ctx, j.JobID, internal.FailureInfoV1{
				Message: fmt.Sprintf("job not found for job_id=%d entity=%s entity_id=%d entity_type=%s",
					j.JobID, e.Name, e.ID, e.Type),
			})
			return nil
		}
		slog.Error("Failed to get job status", "entity", e, "job_id", j.JobID, "err", err)
		return nil
	}

	status := ghJob.Status
	if ghJob.Conclusion != nil && *ghJob.Conclusion != "" {
		status = "completed"
	}

	switch status {
	case "completed":
		slog.Info("GH reconcile: job is completed on GitHub",
			"entity", e, "job_id", j.JobID, "prev_status", j.Status)
		_, _ = a.DB.MarkJobCompleted(ctx, j.JobID, ghJob.RunnerName)
	case "in_progress":
		if j.Status == "pending" {
			slog.Info("GH reconcile: job is in_progress on GitHub (was pending in DB)",
				"entity", e, "job_id", j.JobID)
			_, _ = a.DB.MarkJobRunning(ctx, j.JobID, ghJob.RunnerName)
		}
	case "queued":
		a.reconcileStuckQueued(ctx, j, ghJob, token)
	}
	return nil
}

// reconcileStuckQueued detects jobs that GitHub still reports as queued
// even though their parent workflow run has already terminated. This
// happens when a run is cancelled (or a sibling fails fast-fail-style)
// before scheduling reaches the job; the job then sits queued forever
// and the scheduler would otherwise keep trying to provision a runner
// for a job that will never start. We mark the row failed with the
// run's conclusion so the worker slot frees up.
func (a *App) reconcileStuckQueued(ctx context.Context, j internal.Job, ghJob internal.GHJob, token string) {
	if ghJob.RunID == 0 || time.Since(j.CreatedAt) < internal.JobStuckQueuedMinAge {
		return
	}
	run, err := a.GH.GetRunInfo(ctx, token, j.RepoFullName, ghJob.RunID)
	if err != nil {
		slog.Debug("GetRunInfo failed", "entity", j.Entity(), "job_id", j.JobID, "run_id", ghJob.RunID, "err", err)
		return
	}
	if run.Status != "completed" {
		return
	}
	conclusion := "unknown"
	if run.Conclusion != nil && *run.Conclusion != "" {
		conclusion = *run.Conclusion
	}
	slog.Warn("GH reconcile: job stuck queued while run is completed; marking failed",
		"entity", j.Entity(), "job_id", j.JobID, "run_id", ghJob.RunID, "run_conclusion", conclusion)
	_, _ = a.DB.MarkJobFailed(ctx, j.JobID, internal.FailureInfoV1{
		Message: fmt.Sprintf("workflow run %d completed (conclusion=%s) while job %d stayed queued",
			ghJob.RunID, conclusion, j.JobID),
	})
}
