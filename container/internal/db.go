package internal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgDB wires the DB interface to a pool. A separate connection (listenConn)
// holds the LISTEN session for the {schema}_queue_event channel.
type pgDB struct {
	pool       pgxPool
	schema     string
	listenConn listenConn
}

// pgxPool is the subset of *pgxpool.Pool used by pgDB. pgxmock.PgxPoolIface
// implements the same surface so tests can drive every query path without a
// live Postgres.
type pgxPool interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
	Close()
}

// listenConn is the subset of *pgx.Conn used for LISTEN/NOTIFY. Tests stub it.
type listenConn interface {
	WaitForNotification(ctx context.Context) (*pgconn.Notification, error)
	Close(ctx context.Context) error
}

// queryer is the subset of pgx methods we use; matches both pgxPool and
// pgx.Tx so writes/reads can run inside WithWorkerLock's transaction.
type queryer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txCtxKey struct{}

// withTx attaches a transaction to ctx so nested DB calls see it.
func withTx(ctx context.Context, tx pgx.Tx) context.Context {
	return context.WithValue(ctx, txCtxKey{}, tx)
}

// q picks the right queryer for ctx: pinned tx if WithWorkerLock is active,
// otherwise a fresh pool connection.
func (d *pgDB) q(ctx context.Context) queryer {
	if tx, ok := ctx.Value(txCtxKey{}).(pgx.Tx); ok {
		return tx
	}
	return d.pool
}

// OpenDB creates the pool, sets default search_path on each conn, and opens
// the LISTEN connection. The returned DB satisfies the internal.DB interface.
func OpenDB(ctx context.Context, cfg Config) (DB, error) {
	pcfg, err := pgxpool.ParseConfig(cfg.PostgresURL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres url: %w", err)
	}
	pcfg.MaxConns = PostgresMaxConn
	schema := cfg.PostgresSchema
	pcfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET search_path TO "+schema)
		return err
	}

	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	lconn, err := pgx.Connect(ctx, cfg.PostgresURL)
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("listen connect: %w", err)
	}
	if _, err := lconn.Exec(ctx, "SET search_path TO "+schema); err != nil {
		lconn.Close(ctx)
		pool.Close()
		return nil, fmt.Errorf("listen set search_path: %w", err)
	}
	if _, err := lconn.Exec(ctx, "LISTEN "+schema+"_queue_event"); err != nil {
		lconn.Close(ctx)
		pool.Close()
		return nil, fmt.Errorf("LISTEN: %w", err)
	}

	return &pgDB{pool: pool, schema: schema, listenConn: lconn}, nil
}

func (d *pgDB) Close() {
	if d.listenConn != nil {
		_ = d.listenConn.Close(context.Background())
	}
	d.pool.Close()
}

// WaitForJob blocks until a NOTIFY arrives on {schema}_queue_event or
// timeout elapses. Drains buffered notifications before returning so the
// scheduler isn't immediately re-woken for events captured while it was busy.
func (d *pgDB) WaitForJob(ctx context.Context, timeout time.Duration) error {
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err := d.listenConn.WaitForNotification(wctx)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		return err
	}
	// Drain any extras non-blockingly.
	drainCtx, drainCancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer drainCancel()
	for {
		if _, err := d.listenConn.WaitForNotification(drainCtx); err != nil {
			return nil
		}
	}
}

// WithWorkerLock serializes demand-match across scheduler containers. The
// transaction holds LOCK TABLE workers IN EXCLUSIVE MODE for the full
// callback; the tx is attached to ctx so every nested write (mark_worker_*,
// add_worker, …) runs on the same conn and stays under the lock without
// self-deadlocking on the pool.
func (d *pgDB) WithWorkerLock(ctx context.Context, fn func(ctx context.Context) error) error {
	tx, err := d.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, "LOCK TABLE workers IN EXCLUSIVE MODE"); err != nil {
		return fmt.Errorf("LOCK TABLE workers: %w", err)
	}
	if err := fn(withTx(ctx, tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// --- Job writes ---

// AddJob inserts a job row and emits NOTIFY on success. labels are sorted
// at write time so demand-match's equality query matches regardless of input order.
func (d *pgDB) AddJob(ctx context.Context, j Job, labels []string) (bool, error) {
	sortedLabels := SortedJSON(labels)
	now := time.Now().UTC()
	htmlURL := ""
	if j.HTMLURL != nil {
		htmlURL = *j.HTMLURL
	}
	tag, err := d.q(ctx).Exec(ctx, `
		INSERT INTO jobs (job_id, status, provider, entity_id, entity_name, entity_type,
		                  repo_full_name, installation_id, job_labels, k8s_pool,
		                  k8s_image, html_url, created_at, updated_at)
		VALUES ($1, 'pending', $2, $3, $4, $5, $6, $7, $8::jsonb, $9, $10, $11, $12, $12)
		ON CONFLICT (job_id) DO NOTHING
	`, j.JobID, j.Provider, j.EntityID, j.EntityName, j.EntityType,
		j.RepoFullName, j.InstallationID, sortedLabels, j.K8sPool, j.K8sImage, htmlURL, now)
	if err != nil {
		return false, err
	}
	created := tag.RowsAffected() > 0
	if created {
		// NOTIFY is best-effort; the scheduler polls every PollInterval so a
		// missed wake-up at worst delays pickup by one tick.
		_, _ = d.q(ctx).Exec(ctx, fmt.Sprintf("NOTIFY %s_queue_event, $1", d.schema), fmt.Sprint(j.JobID))
	}
	return created, nil
}

// markJobStatus encapsulates the pending→running and pending|running→completed paths.
func (d *pgDB) markJobStatus(ctx context.Context, jobID int64, runnerName, newStatus, allowedClause string) (string, error) {
	q := d.q(ctx)
	var prev *string
	err := q.QueryRow(ctx, fmt.Sprintf(`
		WITH prev AS (SELECT status FROM jobs WHERE job_id = $1)
		UPDATE jobs
		SET status = '%s',
		    k8s_pod = COALESCE(k8s_pod, $2),
		    updated_at = now()
		WHERE job_id = $1 AND %s
		RETURNING (SELECT status::text FROM prev) as prev_status
	`, newStatus, allowedClause), jobID, runnerName).Scan(&prev)
	if err == nil && prev != nil {
		return *prev, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	// UPDATE matched nothing — look up current status to return the right value.
	var current *string
	err = q.QueryRow(ctx, "SELECT status::text FROM jobs WHERE job_id = $1", jobID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) || current == nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return *current, nil
}

func (d *pgDB) MarkJobRunning(ctx context.Context, jobID int64, runnerName string) (string, error) {
	return d.markJobStatus(ctx, jobID, runnerName, "running", "status = 'pending'")
}

func (d *pgDB) MarkJobCompleted(ctx context.Context, jobID int64, runnerName string) (string, error) {
	return d.markJobStatus(ctx, jobID, runnerName, "completed", "(status = 'pending' OR status = 'running')")
}

func (d *pgDB) MarkJobFailed(ctx context.Context, jobID int64, info FailureInfo) (string, error) {
	if info.Version == 0 {
		return "", errors.New("failure_info.version must be set")
	}
	infoJSON, err := json.Marshal(info)
	if err != nil {
		return "", err
	}
	q := d.q(ctx)
	var prev *string
	err = q.QueryRow(ctx, `
		WITH prev AS (SELECT status FROM jobs WHERE job_id = $1)
		UPDATE jobs SET status = 'failed', failure_info = $2::jsonb, updated_at = now()
		WHERE job_id = $1 AND (status = 'pending' OR status = 'running')
		RETURNING (SELECT status::text FROM prev) as prev_status
	`, jobID, string(infoJSON)).Scan(&prev)
	if err == nil && prev != nil {
		return *prev, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var current *string
	err = q.QueryRow(ctx, "SELECT status::text FROM jobs WHERE job_id = $1", jobID).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) || current == nil {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return *current, nil
}

// --- Job reads ---

func (d *pgDB) JobExistsForPod(ctx context.Context, podName string) (bool, error) {
	var x int
	err := d.q(ctx).QueryRow(ctx, "SELECT 1 FROM jobs WHERE k8s_pod = $1 LIMIT 1", podName).Scan(&x)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func (d *pgDB) scanJobs(rows pgx.Rows) ([]Job, error) {
	defer rows.Close()
	var out []Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.JobID, &j.Status, &j.FailureInfo, &j.Provider, &j.EntityID, &j.EntityName,
			&j.EntityType, &j.RepoFullName, &j.InstallationID, &j.JobLabels, &j.K8sPool,
			&j.K8sImage, &j.K8sPod, &j.HTMLURL, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (d *pgDB) GetActiveJobs(ctx context.Context) ([]Job, error) {
	rows, err := d.q(ctx).Query(ctx, `SELECT job_id, status, failure_info, provider, entity_id, entity_name,
			entity_type, repo_full_name, installation_id, job_labels, k8s_pool,
			k8s_image, k8s_pod, html_url, created_at, updated_at FROM jobs
		WHERE status = 'pending' OR status = 'running' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return d.scanJobs(rows)
}

func (d *pgDB) GetPendingJobs(ctx context.Context) ([]Job, error) {
	rows, err := d.q(ctx).Query(ctx, `SELECT job_id, status, failure_info, provider, entity_id, entity_name,
			entity_type, repo_full_name, installation_id, job_labels, k8s_pool,
			k8s_image, k8s_pod, html_url, created_at, updated_at FROM jobs
		WHERE status = 'pending' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return d.scanJobs(rows)
}

func (d *pgDB) GetAllJobs(ctx context.Context, start, end string, page, perPage int) ([]Job, int, error) {
	where, args := buildDateWhere(start, end)
	var total int
	if err := d.q(ctx).QueryRow(ctx, "SELECT COUNT(*) FROM jobs "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), perPage, page*perPage)
	rows, err := d.q(ctx).Query(ctx, `SELECT job_id, status, failure_info, provider, entity_id, entity_name,
			entity_type, repo_full_name, installation_id, job_labels, k8s_pool,
			k8s_image, k8s_pod, html_url, created_at, updated_at FROM jobs `+where+`
		ORDER BY created_at DESC LIMIT $`+fmt.Sprint(len(args)+1)+` OFFSET $`+fmt.Sprint(len(args)+2),
		pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	jobs, err := d.scanJobs(rows)
	return jobs, total, err
}

// buildDateWhere returns the SQL fragment and args for the optional date filter
// used by /jobs and /workers paginated endpoints.
func buildDateWhere(start, end string) (string, []any) {
	var clauses []string
	var args []any
	if start != "" {
		args = append(args, start)
		clauses = append(clauses, fmt.Sprintf("created_at >= $%d::timestamptz", len(args)))
	}
	if end != "" {
		args = append(args, end)
		clauses = append(clauses, fmt.Sprintf("created_at < $%d::timestamptz", len(args)))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// --- Pool/capacity helpers ---

func (d *pgDB) GetPoolDemand(ctx context.Context, entityID int64, labels []string) (int, int, error) {
	sortedLabels := SortedJSON(labels)
	var jobCount, workerCount int
	err := d.q(ctx).QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM jobs
			 WHERE entity_id = $1 AND job_labels = $2::jsonb
			   AND (status = 'pending' OR status = 'running')) AS job_count,
			(SELECT COUNT(*) FROM workers
			 WHERE entity_id = $1 AND job_labels = $2::jsonb
			   AND (status = 'pending' OR status = 'running')) AS worker_count
	`, entityID, sortedLabels).Scan(&jobCount, &workerCount)
	return jobCount, workerCount, err
}

func (d *pgDB) GetTotalWorkersForEntity(ctx context.Context, entityID int64) (int, error) {
	var n int
	err := d.q(ctx).QueryRow(ctx, `
		SELECT COUNT(*) FROM workers
		WHERE entity_id = $1 AND (status = 'pending' OR status = 'running')
	`, entityID).Scan(&n)
	return n, err
}

// --- Workers ---

func (d *pgDB) AddWorker(ctx context.Context, w Worker, labels []string) error {
	sortedLabels := SortedJSON(labels)
	tag, err := d.q(ctx).Exec(ctx, `
		INSERT INTO workers (pod_name, provider, entity_id, entity_name, entity_type,
		                     installation_id, repo_full_name, k8s_pool, job_labels,
		                     k8s_image, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, 'pending', now(), now())
		ON CONFLICT (pod_name) DO NOTHING
	`, w.PodName, w.Provider, w.EntityID, w.EntityName, w.EntityType,
		w.InstallationID, w.RepoFullName, w.K8sPool, sortedLabels, w.K8sImage)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrDuplicatePodName
	}
	return nil
}

func (d *pgDB) MarkWorkerRunning(ctx context.Context, podName, node string, runningAt *time.Time) error {
	_, err := d.q(ctx).Exec(ctx, `
		UPDATE workers
		SET status = 'running',
		    k8s_node = $1,
		    running_at = COALESCE(running_at, $2, now()),
		    updated_at = now()
		WHERE pod_name = $3 AND status = 'pending'
	`, node, runningAt, podName)
	return err
}

func (d *pgDB) MarkWorkerCompleted(ctx context.Context, podName, node string, completedAt *time.Time) error {
	_, err := d.q(ctx).Exec(ctx, `
		UPDATE workers
		SET status = 'completed',
		    k8s_node = COALESCE(k8s_node, $1),
		    completed_at = COALESCE(completed_at, $2, now()),
		    updated_at = now()
		WHERE pod_name = $3 AND (status = 'pending' OR status = 'running')
	`, node, completedAt, podName)
	return err
}

func (d *pgDB) MarkWorkerFailed(ctx context.Context, podName, node string, info FailureInfo, completedAt *time.Time) error {
	if info.Version == 0 {
		return errors.New("failure_info.version must be set")
	}
	infoJSON, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, err = d.q(ctx).Exec(ctx, `
		UPDATE workers
		SET status = 'failed',
		    k8s_node = COALESCE(k8s_node, $1),
		    failure_info = $2::jsonb,
		    completed_at = COALESCE($3, now()),
		    updated_at = now()
		WHERE pod_name = $4 AND (status = 'pending' OR status = 'running')
	`, node, string(infoJSON), completedAt, podName)
	return err
}

func (d *pgDB) MarkWorkerOrphaned(ctx context.Context, podName string) error {
	_, err := d.q(ctx).Exec(ctx, `
		UPDATE workers
		SET status = 'completed',
		    completed_at = COALESCE(completed_at, now()),
		    updated_at = now()
		WHERE pod_name = $1 AND (status = 'pending' OR status = 'running')
	`, podName)
	return err
}

func (d *pgDB) scanWorkers(rows pgx.Rows) ([]Worker, error) {
	defer rows.Close()
	var out []Worker
	for rows.Next() {
		var w Worker
		if err := rows.Scan(&w.PodName, &w.Provider, &w.EntityID, &w.EntityName, &w.EntityType,
			&w.InstallationID, &w.RepoFullName, &w.JobLabels, &w.K8sPool, &w.K8sImage, &w.K8sNode,
			&w.Status, &w.FailureInfo, &w.CreatedAt, &w.RunningAt, &w.CompletedAt, &w.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (d *pgDB) GetActiveJobsAndWorkers(ctx context.Context) ([]Job, []Worker, error) {
	jobs, err := d.GetActiveJobs(ctx)
	if err != nil {
		return nil, nil, err
	}
	workers, err := d.GetActiveWorkers(ctx)
	return jobs, workers, err
}

func (d *pgDB) GetActiveWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := d.q(ctx).Query(ctx, `SELECT pod_name, provider, entity_id, entity_name, entity_type,
			installation_id, repo_full_name, job_labels, k8s_pool, k8s_image, k8s_node,
			status, failure_info, created_at, running_at, completed_at, updated_at FROM workers
		WHERE status = 'pending' OR status = 'running' ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	return d.scanWorkers(rows)
}

func (d *pgDB) GetAllWorkers(ctx context.Context, start, end string, page, perPage int) ([]Worker, int, error) {
	where, args := buildDateWhere(start, end)
	var total int
	if err := d.q(ctx).QueryRow(ctx, "SELECT COUNT(*) FROM workers "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	pageArgs := append(append([]any{}, args...), perPage, page*perPage)
	rows, err := d.q(ctx).Query(ctx, `SELECT pod_name, provider, entity_id, entity_name, entity_type,
			installation_id, repo_full_name, job_labels, k8s_pool, k8s_image, k8s_node,
			status, failure_info, created_at, running_at, completed_at, updated_at FROM workers `+where+`
		ORDER BY created_at DESC LIMIT $`+fmt.Sprint(len(args)+1)+` OFFSET $`+fmt.Sprint(len(args)+2),
		pageArgs...)
	if err != nil {
		return nil, 0, err
	}
	workers, err := d.scanWorkers(rows)
	return workers, total, err
}

func (d *pgDB) GetWorkersForReconcile(ctx context.Context, terminalLookback time.Duration) ([]Worker, error) {
	rows, err := d.q(ctx).Query(ctx, `
		SELECT pod_name, provider, entity_id, entity_name, entity_type,
			installation_id, repo_full_name, job_labels, k8s_pool, k8s_image, k8s_node,
			status, failure_info, created_at, running_at, completed_at, updated_at FROM workers
		WHERE status IN ('pending', 'running')
		   OR (status IN ('completed', 'failed')
		       AND completed_at IS NOT NULL
		       AND completed_at > now() - ($1 || ' seconds')::interval)
	`, int(terminalLookback.Seconds()))
	if err != nil {
		return nil, err
	}
	return d.scanWorkers(rows)
}

// --- Installation events ---

func (d *pgDB) AddInstallationEvent(ctx context.Context, e InstallationEvent, payload []byte) (int64, error) {
	if payload == nil {
		payload = []byte("{}")
	}
	var id int64
	err := d.q(ctx).QueryRow(ctx, `
		INSERT INTO installation_events
			(source, event, outcome, installation_id, app_id, entity_type,
			 entity_id, entity_name, payload)
		VALUES ($1, $2, $3, $4, $5, $6::entity_type_enum, $7, $8, $9::jsonb)
		RETURNING id
	`, e.Source, e.Event, e.Outcome, e.InstallationID, e.AppID, e.EntityType,
		e.EntityID, e.EntityName, string(payload)).Scan(&id)
	return id, err
}

func (d *pgDB) GetEventsByEntityID(ctx context.Context, entityID int64) ([]InstallationEvent, error) {
	rows, err := d.q(ctx).Query(ctx, `
		SELECT id, source, event, outcome, installation_id, app_id, entity_type,
		       entity_id, entity_name, received_at,
		       CASE WHEN event LIKE 'workflow_job.%%'
		            THEN payload->'workflow_job'->>'id' END AS job_id,
		       CASE WHEN event LIKE 'workflow_job.%%'
		            THEN payload->'repository'->>'full_name' END AS repo_full_name
		FROM installation_events
		WHERE entity_id = $1
		ORDER BY received_at
	`, entityID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstallationEvent
	for rows.Next() {
		var e InstallationEvent
		if err := rows.Scan(&e.ID, &e.Source, &e.Event, &e.Outcome, &e.InstallationID, &e.AppID,
			&e.EntityType, &e.EntityID, &e.EntityName, &e.ReceivedAt, &e.JobID, &e.RepoFullName); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *pgDB) GetPayloadByID(ctx context.Context, eventID int64) ([]byte, error) {
	var payload []byte
	err := d.q(ctx).QueryRow(ctx, "SELECT payload FROM installation_events WHERE id = $1", eventID).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return payload, err
}

func (d *pgDB) GetEntityIDForInstallation(ctx context.Context, installationID int64) (int64, bool, error) {
	var eid int64
	err := d.q(ctx).QueryRow(ctx, `
		SELECT entity_id FROM installation_events
		WHERE installation_id = $1 AND entity_id IS NOT NULL
		ORDER BY received_at DESC LIMIT 1
	`, installationID).Scan(&eid)
	if err == nil {
		return eid, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	err = d.q(ctx).QueryRow(ctx, `
		SELECT entity_id FROM jobs WHERE installation_id = $1 ORDER BY created_at DESC LIMIT 1
	`, installationID).Scan(&eid)
	if err == nil {
		return eid, true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return 0, false, err
}

func (d *pgDB) GetEntityIDForJob(ctx context.Context, jobID int64) (int64, bool, error) {
	var eid int64
	err := d.q(ctx).QueryRow(ctx, "SELECT entity_id FROM jobs WHERE job_id = $1", jobID).Scan(&eid)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	return eid, err == nil, err
}
