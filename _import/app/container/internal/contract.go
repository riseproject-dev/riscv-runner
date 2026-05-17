package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// EntityType matches the SQL TEXT column on jobs / workers / installation_events.
type EntityType string

const (
	EntityOrganization EntityType = "Organization"
	EntityUser         EntityType = "User"
)

func ParseEntityType(s string) (EntityType, error) {
	switch s {
	case "Organization":
		return EntityOrganization, nil
	case "User":
		return EntityUser, nil
	default:
		return "", fmt.Errorf("unsupported entity type: %q", s)
	}
}

// Entity identifies a GitHub org or user. Jobs / workers / installation_events
// all hang off this trio; pass it together so log lines, auth context, and DB
// rows stay in sync.
type Entity struct {
	Type EntityType
	Name string
	ID   int64
}

// LogValue makes Entity an slog.LogValuer. Pass it as a single attr and the
// text handler emits entity.type / entity.name / entity.id (in that order).
func (e Entity) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("type", string(e.Type)),
		slog.String("name", e.Name),
		slog.Int64("id", e.ID),
	)
}

// WebhookOutcome is stored verbatim in installation_events.outcome.
type WebhookOutcome string

const (
	OutcomeOK                 WebhookOutcome = "ok"
	OutcomeJobStored          WebhookOutcome = "job_stored"
	OutcomeJobAlreadyExists   WebhookOutcome = "job_already_exists"
	OutcomeJobMarkedRunning   WebhookOutcome = "job_marked_running"
	OutcomeJobMarkedCompleted WebhookOutcome = "job_marked_completed"
	OutcomeJobNotFound        WebhookOutcome = "job_not_found"
	OutcomeIgnoredAction      WebhookOutcome = "ignored_action"
	OutcomeIgnoredNoLabel     WebhookOutcome = "ignored_no_label"
	OutcomeIgnoredEvent       WebhookOutcome = "ignored_event"
	OutcomeProxiedToStaging   WebhookOutcome = "proxied_to_staging"
	OutcomeAuth404            WebhookOutcome = "auth_404"
	OutcomeAuthOtherError     WebhookOutcome = "auth_other_error"
)

// FailureReason describes why a worker entered the failed state.
type FailureReason string

const (
	ReasonPodAllocationFailure  FailureReason = "pod_allocation_failure"
	ReasonPodFailed             FailureReason = "pod_failed"
	ReasonPodStuckPending       FailureReason = "pod_stuck_pending"
	ReasonRunnerNeverRegistered FailureReason = "runner_never_registered"
	ReasonRunnerIdle            FailureReason = "runner_idle"
)

// Job is one row of the jobs table.
type Job struct {
	JobID          int64           `db:"job_id" json:"job_id"`
	Status         string          `db:"status" json:"status"`
	FailureInfo    json.RawMessage `db:"failure_info" json:"failure_info,omitempty"`
	Provider       string          `db:"provider" json:"provider"`
	EntityID       int64           `db:"entity_id" json:"entity_id"`
	EntityName     string          `db:"entity_name" json:"entity_name"`
	EntityType     string          `db:"entity_type" json:"entity_type"`
	RepoFullName   string          `db:"repo_full_name" json:"repo_full_name"`
	InstallationID int64           `db:"installation_id" json:"installation_id"`
	JobLabels      json.RawMessage `db:"job_labels" json:"job_labels"`
	K8sPool        string          `db:"k8s_pool" json:"k8s_pool"`
	K8sImage       string          `db:"k8s_image" json:"k8s_image"`
	K8sPod         *string         `db:"k8s_pod" json:"k8s_pod,omitempty"`
	HTMLURL        *string         `db:"html_url" json:"html_url,omitempty"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updated_at"`
}

// Entity bundles the three identifying fields. Keep the flat DB columns for
// the SQL roundtrip and JSON shape UI consumers depend on (invariant 1055cc8).
func (j Job) Entity() Entity {
	return Entity{Type: EntityType(j.EntityType), Name: j.EntityName, ID: j.EntityID}
}

// Worker is one row of the workers table.
type Worker struct {
	PodName        string          `db:"pod_name" json:"pod_name"`
	Provider       string          `db:"provider" json:"provider"`
	EntityID       int64           `db:"entity_id" json:"entity_id"`
	EntityName     string          `db:"entity_name" json:"entity_name"`
	EntityType     string          `db:"entity_type" json:"entity_type"`
	InstallationID int64           `db:"installation_id" json:"installation_id"`
	RepoFullName   *string         `db:"repo_full_name" json:"repo_full_name,omitempty"`
	JobLabels      json.RawMessage `db:"job_labels" json:"job_labels"`
	K8sPool        string          `db:"k8s_pool" json:"k8s_pool"`
	K8sImage       string          `db:"k8s_image" json:"k8s_image"`
	K8sNode        *string         `db:"k8s_node" json:"k8s_node,omitempty"`
	Status         string          `db:"status" json:"status"`
	FailureInfo    json.RawMessage `db:"failure_info" json:"failure_info,omitempty"`
	CreatedAt      time.Time       `db:"created_at" json:"created_at"`
	RunningAt      *time.Time      `db:"running_at" json:"running_at,omitempty"`
	CompletedAt    *time.Time      `db:"completed_at" json:"completed_at,omitempty"`
	UpdatedAt      time.Time       `db:"updated_at" json:"updated_at"`
}

func (w Worker) Entity() Entity {
	return Entity{Type: EntityType(w.EntityType), Name: w.EntityName, ID: w.EntityID}
}

// InstallationEvent is one row of installation_events (read shape).
type InstallationEvent struct {
	ID             int64           `db:"id" json:"id"`
	Source         string          `db:"source" json:"source"`
	Event          string          `db:"event" json:"event"`
	Outcome        string          `db:"outcome" json:"outcome"`
	InstallationID *int64          `db:"installation_id" json:"installation_id,omitempty"`
	AppID          *int64          `db:"app_id" json:"app_id,omitempty"`
	EntityType     *string         `db:"entity_type" json:"entity_type,omitempty"`
	EntityID       *int64          `db:"entity_id" json:"entity_id,omitempty"`
	EntityName     *string         `db:"entity_name" json:"entity_name,omitempty"`
	ReceivedAt     time.Time       `db:"received_at" json:"received_at"`
	JobID          *string         `db:"job_id" json:"job_id,omitempty"`
	RepoFullName   *string         `db:"repo_full_name" json:"repo_full_name,omitempty"`
	Payload        json.RawMessage `db:"payload" json:"-"`
}

// FailureInfo is the sealed interface implemented by FailureInfoV1 and
// FailureInfoV2. Two on-disk schemas coexist for workers.failure_info /
// jobs.failure_info, and which one is correct depends on the failure
// mode:
//
//   - FailureInfoV1 (non-pod failures): {"version":1,"message":"..."} --
//     a free-form human message with no structured fields. Used by the
//     scheduler when the failure isn't a k8s pod outcome (installation
//     404, job missing on GitHub, run completed with a queued job, ...).
//   - FailureInfoV2 (pod failures): {"version":2, "reason":<enum>,
//     "pod_reason":..., "pod_message":..., "containers":..., "events":...}
//     populated from CollectPodFailureInfo. Reason is a typed enum value
//     (ReasonPodFailed, ReasonPodStuckPending, ...).
//
// DB.Mark{Job,Worker}Failed accept the interface so a caller can pass
// either variant; the on-disk shape is determined by the concrete type.
// Renderers must look at "version" in the parsed JSON first and pick
// the right branch.
type FailureInfo interface {
	isFailureInfo()
}

// FailureInfoV1 is the non-pod failure shape: a free-form message and
// nothing else. Marshals as {"version":1, "message":"..."}.
type FailureInfoV1 struct {
	Message string
}

// FailureInfoV2 is the structured pod-failure shape produced by
// CollectPodFailureInfo. Marshals as {"version":2, "reason":..., ...}.
type FailureInfoV2 struct {
	Reason       FailureReason
	PodReason    string
	PodMessage   string
	Containers   map[string]ContainerInfo
	Events       []EventInfo
	CollectError string
}

func (FailureInfoV1) isFailureInfo() {}
func (FailureInfoV2) isFailureInfo() {}

func (f FailureInfoV1) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Version int    `json:"version"`
		Message string `json:"message,omitempty"`
	}{1, f.Message})
}

func (f FailureInfoV2) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Version      int                      `json:"version"`
		Reason       FailureReason            `json:"reason"`
		PodReason    string                   `json:"pod_reason,omitempty"`
		PodMessage   string                   `json:"pod_message,omitempty"`
		Containers   map[string]ContainerInfo `json:"containers,omitempty"`
		Events       []EventInfo              `json:"events,omitempty"`
		CollectError string                   `json:"collect_error,omitempty"`
	}{2, f.Reason, f.PodReason, f.PodMessage, f.Containers, f.Events, f.CollectError})
}

// ContainerInfo holds termination details + optional logs for one container.
type ContainerInfo struct {
	ExitCode *int32 `json:"exit_code"`
	Reason   string `json:"reason"`
	Message  string `json:"message"`
	Logs     string `json:"logs,omitempty"`
}

// EventInfo captures one Kubernetes pod event.
type EventInfo struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Message   string `json:"message"`
	Count     int32  `json:"count"`
	FirstSeen string `json:"first_seen,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

// Pod is the subset of K8s pod state we consume; KubeClient returns this shape
// so callers don't have to depend on client-go types.
type Pod struct {
	Name            string
	Phase           string // Pending, Running, Succeeded, Failed
	NodeName        string
	Message         string
	Reason          string
	CreationTime    time.Time
	Containers      []ContainerStatus
	InitContainers  []ContainerStatus
	ReadyTransition *time.Time
}

// ContainerStatus is the subset of v1.ContainerStatus the scheduler reads.
type ContainerStatus struct {
	Name           string
	Running        bool
	RunningStarted *time.Time
	Terminated     bool
	TerminatedAt   *time.Time
	ExitCode       *int32
	Reason         string
	Message        string
	Waiting        bool
	WaitingReason  string
	WaitingMessage string
}

// FinishedAt returns the latest container termination time across all
// containers (main + init), or nil when no container has terminated.
func (p Pod) FinishedAt() *time.Time {
	var latest *time.Time
	for _, cs := range append(append([]ContainerStatus{}, p.Containers...), p.InitContainers...) {
		if cs.Terminated && cs.TerminatedAt != nil {
			if latest == nil || cs.TerminatedAt.After(*latest) {
				t := *cs.TerminatedAt
				latest = &t
			}
		}
	}
	return latest
}

// RunnerStartedAt returns when the 'runner' container actually began running,
// falling back to the pod's Ready transition.
func (p Pod) RunnerStartedAt() *time.Time {
	for _, cs := range p.Containers {
		if cs.Name == "runner" && cs.Running && cs.RunningStarted != nil {
			t := *cs.RunningStarted
			return &t
		}
	}
	if p.ReadyTransition != nil {
		t := *p.ReadyTransition
		return &t
	}
	return nil
}

// PodEvent is the trimmed view of v1.Event we surface to callers.
type PodEvent struct {
	Type      string
	Reason    string
	Message   string
	Count     int32
	FirstSeen *time.Time
	LastSeen  *time.Time
}

// GHRunner is the GitHub Actions runner registration row.
type GHRunner struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"` // online, offline, ...
	Busy   bool   `json:"busy"`
}

// GHJob is the subset of the GitHub job-info response we use.
type GHJob struct {
	Status     string  `json:"status"`     // queued, in_progress, completed
	Conclusion *string `json:"conclusion"` // null, success, failure, cancelled, ...
	RunnerName string  `json:"runner_name"`
	RunID      int64   `json:"run_id"`
}

// GHRun is the subset of the GitHub workflow-run response we use.
// A run can finish (status=completed, conclusion!=null) while one of
// its jobs stays stuck in status=queued forever — that's the case the
// scheduler reconciles against.
type GHRun struct {
	Status     string  `json:"status"`
	Conclusion *string `json:"conclusion"`
}

// Installation is the parsed shape of GET /app/installations/{id}.
type Installation struct {
	ID      int64           `json:"id"`
	Account InstallAccount  `json:"account"`
	Raw     json.RawMessage `json:"-"`
}

type InstallAccount struct {
	Login string `json:"login"`
	Type  string `json:"type"`
	ID    int64  `json:"id"`
}

// --- Client interfaces ---
// cmd/ghfe and cmd/scheduler take these by interface so tests can fake them.

// DB is the persistence surface used by ghfe and scheduler.
type DB interface {
	// Lifecycle
	Close()
	WaitForJob(ctx context.Context, timeout time.Duration) error

	// Job writes
	AddJob(ctx context.Context, j Job, labels []string) (bool, error)
	MarkJobRunning(ctx context.Context, jobID int64, runnerName string) (string, error)
	MarkJobCompleted(ctx context.Context, jobID int64, runnerName string) (string, error)
	MarkJobFailed(ctx context.Context, jobID int64, info FailureInfo) (string, error)

	// Job reads
	JobExistsForPod(ctx context.Context, podName string) (bool, error)
	GetActiveJobs(ctx context.Context) ([]Job, error)
	GetPendingJobs(ctx context.Context) ([]Job, error)
	GetAllJobs(ctx context.Context, start, end string, page, perPage int) ([]Job, int, error)

	// Pool / capacity helpers
	GetPoolDemand(ctx context.Context, entityID int64, labels []string) (int, int, error)
	GetTotalWorkersForEntity(ctx context.Context, entityID int64) (int, error)

	// Workers
	AddWorker(ctx context.Context, w Worker, labels []string) error // returns ErrDuplicatePodName on collision
	MarkWorkerRunning(ctx context.Context, podName, node string, runningAt *time.Time) error
	MarkWorkerCompleted(ctx context.Context, podName, node string, completedAt *time.Time) error
	MarkWorkerFailed(ctx context.Context, podName, node string, info FailureInfo, completedAt *time.Time) error
	MarkWorkerOrphaned(ctx context.Context, podName string) error
	GetActiveJobsAndWorkers(ctx context.Context) ([]Job, []Worker, error)
	GetActiveWorkers(ctx context.Context) ([]Worker, error)
	GetAllWorkers(ctx context.Context, start, end string, page, perPage int) ([]Worker, int, error)
	GetWorkersForReconcile(ctx context.Context, terminalLookback time.Duration) ([]Worker, error)

	// Installation events
	AddInstallationEvent(ctx context.Context, e InstallationEvent, payload []byte) (int64, error)
	GetEventsByEntityID(ctx context.Context, entityID int64) ([]InstallationEvent, error)
	GetPayloadByID(ctx context.Context, eventID int64) ([]byte, error)
	GetEntityIDForInstallation(ctx context.Context, installationID int64) (int64, bool, error)
	GetEntityIDForJob(ctx context.Context, jobID int64) (int64, bool, error)

	// Scheduler critical section: LOCK TABLE workers IN EXCLUSIVE MODE for the
	// duration of fn. Nested DB calls on the same DB run within that transaction.
	WithWorkerLock(ctx context.Context, fn func(ctx context.Context) error) error
}

// ErrDuplicatePodName is returned by AddWorker when the pod_name PK collides.
var ErrDuplicatePodName = fmt.Errorf("duplicate pod name")

// GitHubClient is the GitHub App + REST surface used by ghfe and scheduler.
// Implementations must keep the 59-minute installation-token cache.
type GitHubClient interface {
	AuthenticateApp(ctx context.Context, installationID, appID int64) (string, error)
	GetInstallation(ctx context.Context, installationID int64, et EntityType) (Installation, error)

	EnsureRunnerGroup(ctx context.Context, token, orgName, groupName string) (int64, error)
	CreateJITRunnerConfigOrg(ctx context.Context, token, orgName, runnerName string, groupID int64, labels []string) (string, error)
	CreateJITRunnerConfigRepo(ctx context.Context, token, repoFullName, runnerName string, labels []string) (string, error)

	ListRunnersOrgGroup(ctx context.Context, token, orgName string, groupID int64) ([]GHRunner, error)
	ListRunnersRepo(ctx context.Context, token, repoFullName string) ([]GHRunner, error)
	DeleteRunnerOrg(ctx context.Context, token, orgName string, runnerID int64) error
	DeleteRunnerRepo(ctx context.Context, token, repoFullName string, runnerID int64) error

	GetJobInfo(ctx context.Context, token, repoFullName string, jobID int64) (GHJob, error)
	GetRunInfo(ctx context.Context, token, repoFullName string, runID int64) (GHRun, error)
}

// GitHubAPIError carries the HTTP status code so callers can distinguish 404.
type GitHubAPIError struct {
	StatusCode int
	Message    string
}

func (e *GitHubAPIError) Error() string { return e.Message }

// Capacity is the AvailableSlots breakdown so callers can log every number
// that goes into the decision.
type Capacity struct {
	Total     int // allocatable runner resource on matching nodes
	Active    int // count of Pending|Running runner pods
	Available int // Total - Active
}

// KubeClient is the Kubernetes surface used by the scheduler.
type KubeClient interface {
	ProvisionRunner(ctx context.Context, jitConfig, runnerName, image, pool string, entity Entity) error
	ListPods(ctx context.Context) ([]Pod, error)
	GetPodEvents(ctx context.Context, podName string) ([]PodEvent, error)
	GetPodLogs(ctx context.Context, podName, container string) (string, error)
	DeletePod(ctx context.Context, podName string) error
	KillPod(ctx context.Context, podName string) error
	AvailableSlots(ctx context.Context, pool string) (Capacity, error)
	CollectPodFailureInfo(ctx context.Context, pod Pod, reason FailureReason) FailureInfoV2
}

// --- helpers shared between cmd/* ---

// SortedJSON returns labels as a JSONB-compatible string with stable sort.
// Used for jobs.job_labels / workers.job_labels write/match paths.
func SortedJSON(labels []string) string {
	sorted := append([]string{}, labels...)
	// simple sort; lengths are tiny (typically <5)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j-1] > sorted[j]; j-- {
			sorted[j-1], sorted[j] = sorted[j], sorted[j-1]
		}
	}
	b, _ := json.Marshal(sorted)
	return string(b)
}

// AgeSeconds returns seconds since t. nil → +Inf so timeout comparisons
// treat "never set" as stale.
func AgeSeconds(t *time.Time) float64 {
	if t == nil {
		return 1e308
	}
	return time.Since(*t).Seconds()
}
