// SPDX-License-Identifier: MIT

package internal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
)

// newMockDB returns a pgDB wired to a pgxmock pool with regex query matching.
// Tests register expectations on the returned mock and call methods on pgDB.
func newMockDB(t *testing.T) (*pgDB, pgxmock.PgxPoolIface) {
	t.Helper()
	mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("NewPool: %v", err)
	}
	t.Cleanup(mock.Close)
	return &pgDB{pool: mock, schema: "staging"}, mock
}

// anyN returns n pgxmock.AnyArg() placeholders for tests that don't care
// about the literal arg values.
func anyN(n int) []any {
	out := make([]any, n)
	for i := range out {
		out[i] = pgxmock.AnyArg()
	}
	return out
}

// jobScanRow returns one row's worth of jobColumns-shaped values for ScanJobs.
func jobScanRow() []any {
	return []any{
		int64(1), "pending", []byte(`{}`), "github", int64(99), "acme",
		"Organization", "acme/r", int64(7), []byte(`["x"]`), "scw-em-rv1",
		"img", nil, nil, time.Now(), time.Now(),
	}
}

func workerScanRow(name string, status string) []any {
	return []any{
		name, "github", int64(99), "acme", "Organization", int64(7), nil,
		[]byte(`["x"]`), "scw-em-rv1", "img", nil, status, nil,
		time.Now(), nil, nil, time.Now(),
	}
}

func jobColumns() []string {
	return []string{"job_id", "status", "failure_info", "provider", "entity_id",
		"entity_name", "entity_type", "repo_full_name", "installation_id",
		"job_labels", "k8s_pool", "k8s_image", "k8s_pod", "html_url",
		"created_at", "updated_at"}
}

func workerColumns() []string {
	return []string{"pod_name", "provider", "entity_id", "entity_name", "entity_type",
		"installation_id", "repo_full_name", "job_labels", "k8s_pool", "k8s_image", "k8s_node",
		"status", "failure_info", "created_at", "running_at", "completed_at", "updated_at"}
}

func TestAddJob_InsertedAndNotifies(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`INSERT INTO jobs`).WithArgs(anyN(12)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	mock.ExpectExec(`NOTIFY staging_queue_event`).WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("NOTIFY", 0))

	got, err := db.AddJob(context.Background(), Job{JobID: 1, Provider: "github", EntityID: 99,
		EntityName: "acme", EntityType: "Organization", RepoFullName: "acme/r",
		InstallationID: 7, K8sPool: "scw-em-rv1", K8sImage: "img"}, []string{"x"})
	if err != nil || !got {
		t.Fatalf("AddJob: got=%v err=%v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestAddJob_DuplicateReturnsFalse(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`INSERT INTO jobs`).WithArgs(anyN(12)...).WillReturnResult(pgxmock.NewResult("INSERT", 0))
	got, err := db.AddJob(context.Background(), Job{JobID: 1, EntityType: "User"}, nil)
	if err != nil || got {
		t.Fatalf("AddJob duplicate: got=%v err=%v", got, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestAddJob_PropagatesError(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`INSERT INTO jobs`).WithArgs(anyN(12)...).WillReturnError(errors.New("dial"))
	_, err := db.AddJob(context.Background(), Job{JobID: 1}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestMarkJobRunning_ReturnsPrevStatus(t *testing.T) {
	db, mock := newMockDB(t)
	prev := "pending"
	mock.ExpectQuery(`WITH prev AS .*UPDATE jobs.*status = 'running'`).
		WithArgs(int64(1), "runner-x").
		WillReturnRows(pgxmock.NewRows([]string{"prev_status"}).AddRow(&prev))
	prev, err := db.MarkJobRunning(context.Background(), 1, "runner-x")
	if err != nil || prev != "pending" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
}

func TestMarkJobRunning_NoOpReadsCurrent(t *testing.T) {
	db, mock := newMockDB(t)
	cur := "completed"
	mock.ExpectQuery(`UPDATE jobs.*status = 'running'`).
		WithArgs(int64(2), "rn").
		WillReturnRows(pgxmock.NewRows([]string{"prev_status"}).AddRow((*string)(nil)))
	mock.ExpectQuery(`SELECT status::text FROM jobs WHERE job_id`).
		WithArgs(int64(2)).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow(&cur))
	prev, err := db.MarkJobRunning(context.Background(), 2, "rn")
	if err != nil || prev != "completed" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
}

func TestMarkJobRunning_NotFoundReturnsEmpty(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`UPDATE jobs.*status = 'running'`).
		WithArgs(int64(3), "").
		WillReturnRows(pgxmock.NewRows([]string{"prev_status"}).AddRow((*string)(nil)))
	mock.ExpectQuery(`SELECT status::text FROM jobs WHERE job_id`).
		WithArgs(int64(3)).
		WillReturnError(pgx.ErrNoRows)
	prev, err := db.MarkJobRunning(context.Background(), 3, "")
	if err != nil || prev != "" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
}

func TestMarkJobCompleted_AcceptsRunningOrPending(t *testing.T) {
	db, mock := newMockDB(t)
	prev := "running"
	mock.ExpectQuery(`UPDATE jobs.*status = 'completed'`).
		WithArgs(int64(1), "rn").
		WillReturnRows(pgxmock.NewRows([]string{"prev_status"}).AddRow(&prev))
	prev, err := db.MarkJobCompleted(context.Background(), 1, "rn")
	if err != nil || prev != "running" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
}

func TestMarkJobFailed_RequiresNonNil(t *testing.T) {
	db, _ := newMockDB(t)
	_, err := db.MarkJobFailed(context.Background(), 1, nil)
	if err == nil {
		t.Fatal("expected error on nil FailureInfo")
	}
}

func TestMarkJobFailed_Success(t *testing.T) {
	db, mock := newMockDB(t)
	prev := "running"
	mock.ExpectQuery(`UPDATE jobs SET status = 'failed'`).
		WithArgs(int64(1), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"prev_status"}).AddRow(&prev))
	prev, err := db.MarkJobFailed(context.Background(), 1, FailureInfoV2{Reason: ReasonPodFailed})
	if err != nil || prev != "running" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
}

func TestMarkJobFailed_NoMatchFallbackReadsCurrent(t *testing.T) {
	db, mock := newMockDB(t)
	// UPDATE matches nothing → first QueryRow returns nil-prev (no rows).
	mock.ExpectQuery(`UPDATE jobs SET status = 'failed'`).
		WithArgs(int64(1), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	cur := "completed"
	mock.ExpectQuery(`SELECT status::text FROM jobs WHERE job_id`).
		WithArgs(int64(1)).
		WillReturnRows(pgxmock.NewRows([]string{"status"}).AddRow(&cur))
	prev, err := db.MarkJobFailed(context.Background(), 1, FailureInfoV2{Reason: ReasonPodFailed})
	if err != nil || prev != "completed" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
}

func TestMarkJobFailed_NotFoundReturnsEmpty(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`UPDATE jobs SET status = 'failed'`).
		WithArgs(int64(99), pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT status::text FROM jobs WHERE job_id`).
		WithArgs(int64(99)).
		WillReturnError(pgx.ErrNoRows)
	prev, err := db.MarkJobFailed(context.Background(), 99, FailureInfoV2{Reason: ReasonPodFailed})
	if err != nil || prev != "" {
		t.Fatalf("prev=%q err=%v", prev, err)
	}
}

func TestMarkJobFailed_UpdateErrorPropagates(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`UPDATE jobs SET status = 'failed'`).
		WithArgs(int64(1), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))
	_, err := db.MarkJobFailed(context.Background(), 1, FailureInfoV2{Reason: ReasonPodFailed})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestJobExistsForPod(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT 1 FROM jobs WHERE k8s_pod`).
		WithArgs("pod-1").
		WillReturnRows(pgxmock.NewRows([]string{"x"}).AddRow(1))
	got, err := db.JobExistsForPod(context.Background(), "pod-1")
	if err != nil || !got {
		t.Fatalf("got=%v err=%v", got, err)
	}

	mock.ExpectQuery(`SELECT 1 FROM jobs WHERE k8s_pod`).
		WithArgs("nope").
		WillReturnError(pgx.ErrNoRows)
	got, err = db.JobExistsForPod(context.Background(), "nope")
	if err != nil || got {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestGetActiveJobs(t *testing.T) {
	db, mock := newMockDB(t)
	rows := pgxmock.NewRows(jobColumns()).AddRow(jobScanRow()...)
	mock.ExpectQuery(`SELECT .* FROM jobs.*status = 'pending' OR status = 'running'`).WillReturnRows(rows)
	out, err := db.GetActiveJobs(context.Background())
	if err != nil || len(out) != 1 || out[0].JobID != 1 {
		t.Fatalf("got %+v err=%v", out, err)
	}
}

func TestGetPendingJobs(t *testing.T) {
	db, mock := newMockDB(t)
	rows := pgxmock.NewRows(jobColumns()).AddRow(jobScanRow()...)
	mock.ExpectQuery(`SELECT .* FROM jobs.*WHERE status = 'pending' ORDER BY created_at`).WillReturnRows(rows)
	out, err := db.GetPendingJobs(context.Background())
	if err != nil || len(out) != 1 {
		t.Fatalf("got %+v err=%v", out, err)
	}
}

func TestGetAllJobs_Paginated(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM jobs`).
		WillReturnRows(pgxmock.NewRows([]string{"c"}).AddRow(1))
	rows := pgxmock.NewRows(jobColumns()).AddRow(jobScanRow()...)
	mock.ExpectQuery(`SELECT .* FROM jobs.*LIMIT \$1 OFFSET \$2`).
		WithArgs(10, 0).WillReturnRows(rows)
	out, total, err := db.GetAllJobs(context.Background(), "", "", 0, 10)
	if err != nil || total != 1 || len(out) != 1 {
		t.Fatalf("got total=%d out=%+v err=%v", total, out, err)
	}
}

func TestGetAllJobs_WithDateFilter(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM jobs WHERE created_at >= \$1::timestamptz AND created_at < \$2::timestamptz`).
		WithArgs("2026-01-01", "2026-02-01").
		WillReturnRows(pgxmock.NewRows([]string{"c"}).AddRow(0))
	rows := pgxmock.NewRows(jobColumns())
	mock.ExpectQuery(`SELECT .* FROM jobs WHERE .*LIMIT \$3 OFFSET \$4`).
		WithArgs("2026-01-01", "2026-02-01", 50, 100).
		WillReturnRows(rows)
	_, total, err := db.GetAllJobs(context.Background(), "2026-01-01", "2026-02-01", 2, 50)
	if err != nil || total != 0 {
		t.Fatalf("total=%d err=%v", total, err)
	}
}

func TestGetPoolDemand(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT.*job_count.*worker_count`).
		WithArgs(int64(7), `["x"]`).
		WillReturnRows(pgxmock.NewRows([]string{"j", "w"}).AddRow(3, 1))
	j, w, err := db.GetPoolDemand(context.Background(), 7, []string{"x"})
	if err != nil || j != 3 || w != 1 {
		t.Fatalf("j=%d w=%d err=%v", j, w, err)
	}
}

func TestGetTotalWorkersForEntity(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM workers`).
		WithArgs(int64(7)).
		WillReturnRows(pgxmock.NewRows([]string{"c"}).AddRow(4))
	got, err := db.GetTotalWorkersForEntity(context.Background(), 7)
	if err != nil || got != 4 {
		t.Fatalf("got=%d err=%v", got, err)
	}
}

func TestAddWorker_DuplicateError(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`INSERT INTO workers`).WithArgs(anyN(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 0))
	err := db.AddWorker(context.Background(), Worker{PodName: "p"}, nil)
	if !errors.Is(err, ErrDuplicatePodName) {
		t.Fatalf("expected ErrDuplicatePodName, got %v", err)
	}
}

func TestAddWorker_Success(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`INSERT INTO workers`).WithArgs(anyN(10)...).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	if err := db.AddWorker(context.Background(), Worker{PodName: "p"}, nil); err != nil {
		t.Fatalf("AddWorker: %v", err)
	}
}

func TestMarkWorkerRunning(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`UPDATE workers.*status = 'running'`).WithArgs(anyN(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	now := time.Now()
	if err := db.MarkWorkerRunning(context.Background(), "p", "node-1", &now); err != nil {
		t.Fatalf("MarkWorkerRunning: %v", err)
	}
}

func TestMarkWorkerCompleted(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`UPDATE workers.*status = 'completed'`).WithArgs(anyN(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := db.MarkWorkerCompleted(context.Background(), "p", "node-1", nil); err != nil {
		t.Fatalf("MarkWorkerCompleted: %v", err)
	}
}

func TestMarkWorkerFailed_RequiresNonNil(t *testing.T) {
	db, _ := newMockDB(t)
	if err := db.MarkWorkerFailed(context.Background(), "p", "n", nil, nil); err == nil {
		t.Fatal("expected error on nil FailureInfo")
	}
}

func TestMarkWorkerFailed_Success(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`UPDATE workers.*status = 'failed'`).WithArgs(anyN(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := db.MarkWorkerFailed(context.Background(), "p", "n",
		FailureInfoV2{Reason: ReasonPodFailed}, nil); err != nil {
		t.Fatalf("MarkWorkerFailed: %v", err)
	}
}

func TestMarkWorkerOrphaned(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectExec(`UPDATE workers\s+SET status = 'completed'`).WithArgs("p").
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	if err := db.MarkWorkerOrphaned(context.Background(), "p"); err != nil {
		t.Fatalf("MarkWorkerOrphaned: %v", err)
	}
}

func TestGetActiveJobsAndWorkers(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM jobs`).
		WillReturnRows(pgxmock.NewRows(jobColumns()).AddRow(jobScanRow()...))
	mock.ExpectQuery(`SELECT .* FROM workers`).
		WillReturnRows(pgxmock.NewRows(workerColumns()).AddRow(workerScanRow("p", "pending")...))
	jobs, workers, err := db.GetActiveJobsAndWorkers(context.Background())
	if err != nil || len(jobs) != 1 || len(workers) != 1 {
		t.Fatalf("jobs=%d workers=%d err=%v", len(jobs), len(workers), err)
	}
}

func TestGetActiveWorkers(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM workers`).
		WillReturnRows(pgxmock.NewRows(workerColumns()).AddRow(workerScanRow("p", "running")...))
	out, err := db.GetActiveWorkers(context.Background())
	if err != nil || len(out) != 1 {
		t.Fatalf("got %+v err=%v", out, err)
	}
}

func TestGetAllWorkers_Paginated(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM workers`).
		WillReturnRows(pgxmock.NewRows([]string{"c"}).AddRow(0))
	mock.ExpectQuery(`SELECT .* FROM workers`).WithArgs(10, 0).
		WillReturnRows(pgxmock.NewRows(workerColumns()))
	_, total, err := db.GetAllWorkers(context.Background(), "", "", 0, 10)
	if err != nil || total != 0 {
		t.Fatalf("total=%d err=%v", total, err)
	}
}

func TestGetWorkersForReconcile(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT .* FROM workers\s+WHERE status IN`).
		WithArgs(3600.0).
		WillReturnRows(pgxmock.NewRows(workerColumns()).AddRow(workerScanRow("p", "running")...))
	out, err := db.GetWorkersForReconcile(context.Background(), time.Hour)
	if err != nil || len(out) != 1 {
		t.Fatalf("got %+v err=%v", out, err)
	}
}

func TestAddInstallationEvent(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`INSERT INTO installation_events`).WithArgs(anyN(9)...).
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(99)))
	id, err := db.AddInstallationEvent(context.Background(),
		InstallationEvent{Source: "webhook", Event: "ping", Outcome: "ok"},
		[]byte(`{"zen":"hi"}`))
	if err != nil || id != 99 {
		t.Fatalf("id=%d err=%v", id, err)
	}
}

func TestAddInstallationEvent_DefaultEmptyPayload(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`INSERT INTO installation_events`).
		WithArgs("webhook", "x", "ok", (*int64)(nil), (*int64)(nil),
			(*string)(nil), (*int64)(nil), (*string)(nil), "{}").
		WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(int64(1)))
	_, err := db.AddInstallationEvent(context.Background(),
		InstallationEvent{Source: "webhook", Event: "x", Outcome: "ok"}, nil)
	if err != nil {
		t.Fatalf("AddInstallationEvent: %v", err)
	}
}

func TestGetEventsByEntityID(t *testing.T) {
	db, mock := newMockDB(t)
	now := time.Now()
	jobIDStr, repo := "42", "acme/r"
	mock.ExpectQuery(`SELECT.*FROM installation_events.*WHERE entity_id`).
		WithArgs(int64(7)).
		WillReturnRows(pgxmock.NewRows([]string{"id", "source", "event", "outcome",
			"installation_id", "app_id", "entity_type", "entity_id", "entity_name",
			"received_at", "job_id", "repo_full_name"}).
			AddRow(int64(1), "webhook", "workflow_job.queued", "job_stored",
				(*int64)(nil), (*int64)(nil), (*string)(nil), (*int64)(nil), (*string)(nil),
				now, &jobIDStr, &repo))
	out, err := db.GetEventsByEntityID(context.Background(), 7)
	if err != nil || len(out) != 1 || *out[0].JobID != "42" {
		t.Fatalf("got %+v err=%v", out, err)
	}
}

func TestGetPayloadByID(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT payload FROM installation_events`).
		WithArgs(int64(5)).
		WillReturnRows(pgxmock.NewRows([]string{"payload"}).AddRow([]byte(`{"hi":1}`)))
	body, err := db.GetPayloadByID(context.Background(), 5)
	if err != nil || string(body) != `{"hi":1}` {
		t.Fatalf("got %s err=%v", body, err)
	}

	mock.ExpectQuery(`SELECT payload FROM installation_events`).
		WithArgs(int64(0)).WillReturnError(pgx.ErrNoRows)
	body, err = db.GetPayloadByID(context.Background(), 0)
	if err != nil || body != nil {
		t.Fatalf("got %s err=%v", body, err)
	}
}

func TestGetEntityIDForInstallation_FromEvents(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT entity_id FROM installation_events`).
		WithArgs(int64(11)).
		WillReturnRows(pgxmock.NewRows([]string{"entity_id"}).AddRow(int64(99)))
	id, ok, err := db.GetEntityIDForInstallation(context.Background(), 11)
	if err != nil || !ok || id != 99 {
		t.Fatalf("got id=%d ok=%v err=%v", id, ok, err)
	}
}

func TestGetEntityIDForInstallation_FallsBackToJobs(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT entity_id FROM installation_events`).
		WithArgs(int64(11)).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT entity_id FROM jobs`).
		WithArgs(int64(11)).
		WillReturnRows(pgxmock.NewRows([]string{"entity_id"}).AddRow(int64(99)))
	id, ok, err := db.GetEntityIDForInstallation(context.Background(), 11)
	if err != nil || !ok || id != 99 {
		t.Fatalf("got id=%d ok=%v err=%v", id, ok, err)
	}
}

func TestGetEntityIDForInstallation_NotFound(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT entity_id FROM installation_events`).
		WithArgs(int64(0)).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectQuery(`SELECT entity_id FROM jobs`).
		WithArgs(int64(0)).
		WillReturnError(pgx.ErrNoRows)
	_, ok, err := db.GetEntityIDForInstallation(context.Background(), 0)
	if err != nil || ok {
		t.Fatalf("expected not found, got ok=%v err=%v", ok, err)
	}
}

func TestGetEntityIDForJob(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectQuery(`SELECT entity_id FROM jobs WHERE job_id`).
		WithArgs(int64(1)).
		WillReturnRows(pgxmock.NewRows([]string{"entity_id"}).AddRow(int64(7)))
	id, ok, err := db.GetEntityIDForJob(context.Background(), 1)
	if err != nil || !ok || id != 7 {
		t.Fatalf("got id=%d ok=%v err=%v", id, ok, err)
	}

	mock.ExpectQuery(`SELECT entity_id FROM jobs WHERE job_id`).
		WithArgs(int64(0)).
		WillReturnError(pgx.ErrNoRows)
	_, ok, err = db.GetEntityIDForJob(context.Background(), 0)
	if err != nil || ok {
		t.Fatalf("expected not found")
	}
}

func TestWithWorkerLock_CommitsOnSuccess(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE workers IN EXCLUSIVE MODE`).
		WillReturnResult(pgxmock.NewResult("LOCK", 0))
	mock.ExpectCommit()

	called := false
	err := db.WithWorkerLock(context.Background(), func(ctx context.Context) error {
		called = true
		if _, ok := ctx.Value(txCtxKey{}).(pgx.Tx); !ok {
			t.Error("tx not attached to ctx")
		}
		return nil
	})
	if err != nil || !called {
		t.Fatalf("WithWorkerLock: called=%v err=%v", called, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("unmet: %v", err)
	}
}

func TestWithWorkerLock_RollsBackOnFnError(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE workers`).WillReturnResult(pgxmock.NewResult("LOCK", 0))
	mock.ExpectRollback()
	err := db.WithWorkerLock(context.Background(), func(ctx context.Context) error {
		return errors.New("fn boom")
	})
	if err == nil || err.Error() != "fn boom" {
		t.Fatalf("expected fn boom, got %v", err)
	}
}

func TestWithWorkerLock_LockFailureBubbles(t *testing.T) {
	db, mock := newMockDB(t)
	mock.ExpectBegin()
	mock.ExpectExec(`LOCK TABLE workers`).WillReturnError(errors.New("locked"))
	mock.ExpectRollback()
	err := db.WithWorkerLock(context.Background(), func(ctx context.Context) error {
		t.Fatal("fn should not run")
		return nil
	})
	if err == nil {
		t.Fatal("expected lock error")
	}
}

// fakeListenConn lets WaitForJob run without a real Postgres LISTEN socket.
type fakeListenConn struct {
	wait    func(ctx context.Context) (*pgconn.Notification, error)
	closeOk bool
}

func (f *fakeListenConn) WaitForNotification(ctx context.Context) (*pgconn.Notification, error) {
	return f.wait(ctx)
}
func (f *fakeListenConn) Close(ctx context.Context) error {
	f.closeOk = true
	return nil
}

func TestWaitForJob_DeadlineIsSuccess(t *testing.T) {
	db, _ := newMockDB(t)
	db.listenConn = &fakeListenConn{wait: func(ctx context.Context) (*pgconn.Notification, error) {
		<-ctx.Done()
		return nil, context.DeadlineExceeded
	}}
	if err := db.WaitForJob(context.Background(), 5*time.Millisecond); err != nil {
		t.Errorf("DeadlineExceeded should be nil, got %v", err)
	}
}

func TestWaitForJob_NotificationReturns(t *testing.T) {
	db, _ := newMockDB(t)
	calls := 0
	db.listenConn = &fakeListenConn{wait: func(ctx context.Context) (*pgconn.Notification, error) {
		calls++
		if calls == 1 {
			return &pgconn.Notification{Channel: "staging_queue_event", Payload: "1"}, nil
		}
		<-ctx.Done()
		return nil, context.DeadlineExceeded
	}}
	if err := db.WaitForJob(context.Background(), 50*time.Millisecond); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
	if calls < 1 {
		t.Errorf("expected at least one wait call, got %d", calls)
	}
}

func TestClose_TolerantOfMissingListenConn(t *testing.T) {
	db, _ := newMockDB(t)
	db.listenConn = &fakeListenConn{wait: func(ctx context.Context) (*pgconn.Notification, error) { return nil, nil }}
	db.Close() // shouldn't panic
}

func TestSortedJSON_StableOrder(t *testing.T) {
	got := SortedJSON([]string{"b", "a", "c"})
	var arr []string
	_ = json.Unmarshal([]byte(got), &arr)
	if len(arr) != 3 || arr[0] != "a" || arr[1] != "b" || arr[2] != "c" {
		t.Errorf("not sorted: %v", arr)
	}
}
