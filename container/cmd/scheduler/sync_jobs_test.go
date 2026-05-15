package main

import (
	"context"
	"testing"

	"github.com/riseproject-dev/riscv-runner-app/container/internal"
)

// TestSyncJobsState_GetActiveError covers the early error return.
func TestSyncJobsState_GetActiveError(t *testing.T) {
	app, db, _, _ := schedTestApp()
	db.OnGetActiveJobs = func() ([]internal.Job, error) { return nil, errBoom }
	if err := app.syncJobsState(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// TestSyncJobsState_SkipsJobMissingRepo covers the early-return on no RepoFullName.
func TestSyncJobsState_SkipsJobMissingRepo(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	called := false
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { called = true; return "t", nil }
	db.Jobs = []internal.Job{{JobID: 1, EntityName: "a", EntityType: "Organization", InstallationID: 9}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Errorf("AuthenticateApp should not be called when RepoFullName is empty")
	}
}

// TestSyncJobsState_InvalidEntityType skips the job without auth.
func TestSyncJobsState_InvalidEntityType(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	called := false
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { called = true; return "t", nil }
	db.Jobs = []internal.Job{{JobID: 1, RepoFullName: "a/r", EntityName: "a", EntityType: "Bot"}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Errorf("auth should not be called for invalid entity type")
	}
}

// TestSyncOneJob_InstallationNotFoundMarksFailed covers the install 404 path.
func TestSyncOneJob_InstallationNotFoundMarksFailed(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) {
		return "", &internal.GitHubAPIError{StatusCode: 404, Message: "not found"}
	}
	markCalled := false
	db.OnMarkJobFailed = func(id int64, info internal.FailureInfo) (string, error) {
		markCalled = true
		return "pending", nil
	}
	db.Jobs = []internal.Job{{JobID: 1, RepoFullName: "a/r", EntityName: "a", EntityType: "Organization", InstallationID: 9}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !markCalled {
		t.Errorf("expected MarkJobFailed to be called")
	}
}

// TestSyncOneJob_NonAPIAuthErrorLogged covers the non-404 auth error.
func TestSyncOneJob_NonAPIAuthErrorLogged(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { return "", errBoom }
	db.Jobs = []internal.Job{{JobID: 1, RepoFullName: "a/r", EntityName: "a", EntityType: "Organization", InstallationID: 9}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkFailed) != 0 {
		t.Errorf("non-API error should not mark job failed")
	}
}

// TestSyncOneJob_JobNotFoundMarksFailed covers GetJobInfo 404.
func TestSyncOneJob_JobNotFoundMarksFailed(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { return "tok", nil }
	gh.OnGetJobInfo = func(string, string, int64) (internal.GHJob, error) {
		return internal.GHJob{}, &internal.GitHubAPIError{StatusCode: 404, Message: "n"}
	}
	called := false
	db.OnMarkJobFailed = func(int64, internal.FailureInfo) (string, error) { called = true; return "pending", nil }
	db.Jobs = []internal.Job{{JobID: 1, RepoFullName: "a/r", EntityName: "a", EntityType: "Organization", InstallationID: 9}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Errorf("expected MarkJobFailed on GetJobInfo 404")
	}
}

// TestSyncOneJob_JobInfoNonAPIError covers the non-404 GetJobInfo error.
func TestSyncOneJob_JobInfoNonAPIError(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { return "tok", nil }
	gh.OnGetJobInfo = func(string, string, int64) (internal.GHJob, error) { return internal.GHJob{}, errBoom }
	db.Jobs = []internal.Job{{JobID: 1, RepoFullName: "a/r", EntityName: "a", EntityType: "Organization", InstallationID: 9}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.MarkFailed) != 0 {
		t.Errorf("non-API error should not mark job failed")
	}
}

// TestSyncOneJob_CompletedMarksCompleted covers the completed GH status path.
func TestSyncOneJob_CompletedMarksCompleted(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { return "tok", nil }
	gh.OnGetJobInfo = func(string, string, int64) (internal.GHJob, error) {
		return internal.GHJob{Status: "completed", RunnerName: "r"}, nil
	}
	called := false
	db.OnMarkJobComplete = func(int64, string) (string, error) { called = true; return "pending", nil }
	db.Jobs = []internal.Job{{JobID: 1, Status: "pending", RepoFullName: "a/r", EntityName: "a", EntityType: "Organization", InstallationID: 9}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Errorf("expected MarkJobCompleted")
	}
}

// TestSyncOneJob_ConclusionImpliesCompleted covers the conclusion-present branch.
func TestSyncOneJob_ConclusionImpliesCompleted(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { return "tok", nil }
	conc := "failure"
	gh.OnGetJobInfo = func(string, string, int64) (internal.GHJob, error) {
		return internal.GHJob{Status: "in_progress", Conclusion: &conc, RunnerName: "r"}, nil
	}
	called := false
	db.OnMarkJobComplete = func(int64, string) (string, error) { called = true; return "running", nil }
	db.Jobs = []internal.Job{{JobID: 1, Status: "running", RepoFullName: "a/r", EntityName: "a", EntityType: "Organization", InstallationID: 9}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Errorf("expected MarkJobCompleted for in_progress+conclusion")
	}
}

// TestSyncOneJob_InProgressFromPendingPromotesToRunning covers MarkJobRunning.
func TestSyncOneJob_InProgressFromPendingPromotesToRunning(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { return "tok", nil }
	gh.OnGetJobInfo = func(string, string, int64) (internal.GHJob, error) {
		return internal.GHJob{Status: "in_progress", RunnerName: "r"}, nil
	}
	called := false
	db.OnMarkJobRunning = func(int64, string) (string, error) { called = true; return "pending", nil }
	db.Jobs = []internal.Job{{JobID: 1, Status: "pending", RepoFullName: "a/r", EntityName: "a", EntityType: "Organization", InstallationID: 9}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Errorf("expected MarkJobRunning")
	}
}

// TestSyncOneJob_InProgressFromRunningIsNoop covers the no-op branch when DB
// already has running.
func TestSyncOneJob_InProgressFromRunningIsNoop(t *testing.T) {
	app, db, gh, _ := schedTestApp()
	gh.OnAuthenticateApp = func(int64, int64) (string, error) { return "tok", nil }
	gh.OnGetJobInfo = func(string, string, int64) (internal.GHJob, error) {
		return internal.GHJob{Status: "in_progress", RunnerName: "r"}, nil
	}
	called := false
	db.OnMarkJobRunning = func(int64, string) (string, error) { called = true; return "running", nil }
	db.Jobs = []internal.Job{{JobID: 1, Status: "running", RepoFullName: "a/r", EntityName: "a", EntityType: "Organization", InstallationID: 9}}
	if err := app.syncJobsState(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Errorf("MarkJobRunning should be a no-op when status is already running")
	}
}
