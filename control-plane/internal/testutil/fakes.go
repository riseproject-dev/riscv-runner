// SPDX-License-Identifier: MIT

// Package testutil holds in-memory fakes for the DB, GitHub, and Kube
// interfaces. Use them when testing cmd/ghfe and cmd/scheduler.
package testutil

import (
	"context"
	"sync"
	"time"

	"github.com/riseproject-dev/riscv-runner/control-plane/internal"
)

// FakeDB satisfies internal.DB. Public fields are inspected by tests; methods
// are deliberately small so each one mirrors the SQL shape it stands in for.
type FakeDB struct {
	mu sync.Mutex

	Jobs    []internal.Job
	Workers []internal.Worker
	Events  []EventCall

	PendingDemand   map[demandKey]demandResult
	EntityWorkerCnt map[int64]int
	JobExistsByPod  map[string]bool

	OnAddJob          func(internal.GHJob, internal.Entity, string, string, int64, string, string, string, []string) (bool, error)
	OnAddWorker       func(internal.Worker, []string) error
	OnAddEvent        func(internal.InstallationEvent, []byte) (int64, error)
	OnMarkJobRunning  func(internal.GHJob) (string, error)
	OnMarkJobComplete func(internal.GHJob) (string, error)
	OnMarkJobFailed   func(int64, internal.FailureInfo) (string, error)

	OnGetActiveJobs           func() ([]internal.Job, error)
	OnGetPendingJobs          func() ([]internal.Job, error)
	OnGetWorkersReconcile     func(time.Duration) ([]internal.Worker, error)
	OnGetEntityIDInstall      func(int64) (int64, bool, error)
	OnGetEntityIDJob          func(int64) (int64, bool, error)
	OnGetEventsByEntityID     func(int64) ([]internal.InstallationEvent, error)
	OnGetPayloadByID          func(int64) ([]byte, error)
	OnGetActiveJobsAndWorkers func() ([]internal.Job, []internal.Worker, error)
	OnGetAllJobs              func(start, end string, page, perPage int) ([]internal.Job, int, error)
	OnGetAllWorkers           func(start, end string, page, perPage int) ([]internal.Worker, int, error)

	WorkerStatus map[string]string // last-known status; tests poke this
	MarkRunning  []string
	MarkComplete []string
	MarkFailed   []FailCall
	MarkOrphaned []string
	KillCount    int

	LockHeld bool
}

type demandKey struct {
	EntityID int64
	Labels   string
}

type demandResult struct{ Jobs, Workers int }

// EventCall captures one AddInstallationEvent invocation.
type EventCall struct {
	Row     internal.InstallationEvent
	Payload []byte
}

// FailCall captures the args to MarkWorkerFailed.
type FailCall struct {
	PodName     string
	Node        string
	Info        internal.FailureInfo
	CompletedAt *time.Time
}

// NewFakeDB constructs an empty FakeDB with maps initialised.
func NewFakeDB() *FakeDB {
	return &FakeDB{
		PendingDemand:   map[demandKey]demandResult{},
		EntityWorkerCnt: map[int64]int{},
		JobExistsByPod:  map[string]bool{},
		WorkerStatus:    map[string]string{},
	}
}

// SetPoolDemand makes GetPoolDemand return (jobs, workers) for the given key.
func (f *FakeDB) SetPoolDemand(entityID int64, labels []string, jobs, workers int) {
	f.PendingDemand[demandKey{entityID, internal.SortedJSON(labels)}] = demandResult{Jobs: jobs, Workers: workers}
}

func (f *FakeDB) Close()                                                {}
func (f *FakeDB) WaitForJob(ctx context.Context, t time.Duration) error { return nil }

func (f *FakeDB) AddJob(ctx context.Context, gh internal.GHJob, entity internal.Entity,
	provider, repoFullName string, installationID int64,
	k8sPool, k8sImage, htmlURL string, labels []string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.OnAddJob != nil {
		return f.OnAddJob(gh, entity, provider, repoFullName, installationID, k8sPool, k8sImage, htmlURL, labels)
	}
	for _, e := range f.Jobs {
		if e.JobID == gh.ID {
			return false, nil
		}
	}
	j := internal.Job{
		JobID:          gh.ID,
		Provider:       provider,
		EntityID:       entity.ID,
		EntityName:     entity.Name,
		EntityType:     string(entity.Type),
		RepoFullName:   repoFullName,
		InstallationID: installationID,
		K8sPool:        k8sPool,
		K8sImage:       k8sImage,
		JobCreatedAt:   gh.CreatedAt,
	}
	if gh.Name != "" {
		name := gh.Name
		j.JobName = &name
	}
	if htmlURL != "" {
		u := htmlURL
		j.HTMLURL = &u
	}
	f.Jobs = append(f.Jobs, j)
	return true, nil
}

func (f *FakeDB) MarkJobRunning(ctx context.Context, gh internal.GHJob) (string, error) {
	if f.OnMarkJobRunning != nil {
		return f.OnMarkJobRunning(gh)
	}
	return "pending", nil
}

func (f *FakeDB) MarkJobCompleted(ctx context.Context, gh internal.GHJob) (string, error) {
	if f.OnMarkJobComplete != nil {
		return f.OnMarkJobComplete(gh)
	}
	return "running", nil
}

func (f *FakeDB) MarkJobFailed(ctx context.Context, jobID int64, info internal.FailureInfo) (string, error) {
	if f.OnMarkJobFailed != nil {
		return f.OnMarkJobFailed(jobID, info)
	}
	return "pending", nil
}

func (f *FakeDB) JobExistsForPod(ctx context.Context, pod string) (bool, error) {
	return f.JobExistsByPod[pod], nil
}

func (f *FakeDB) GetActiveJobs(ctx context.Context) ([]internal.Job, error) {
	if f.OnGetActiveJobs != nil {
		return f.OnGetActiveJobs()
	}
	return f.Jobs, nil
}

func (f *FakeDB) GetPendingJobs(ctx context.Context) ([]internal.Job, error) {
	if f.OnGetPendingJobs != nil {
		return f.OnGetPendingJobs()
	}
	out := []internal.Job{}
	for _, j := range f.Jobs {
		if j.Status == "" || j.Status == "pending" {
			out = append(out, j)
		}
	}
	return out, nil
}

func (f *FakeDB) GetAllJobs(ctx context.Context, start, end string, page, perPage int) ([]internal.Job, int, error) {
	if f.OnGetAllJobs != nil {
		return f.OnGetAllJobs(start, end, page, perPage)
	}
	return f.Jobs, len(f.Jobs), nil
}

func (f *FakeDB) GetPoolDemand(ctx context.Context, entityID int64, labels []string) (int, int, error) {
	key := demandKey{entityID, internal.SortedJSON(labels)}
	if r, ok := f.PendingDemand[key]; ok {
		return r.Jobs, r.Workers, nil
	}
	return 0, 0, nil
}

func (f *FakeDB) GetTotalWorkersForEntity(ctx context.Context, entityID int64) (int, error) {
	return f.EntityWorkerCnt[entityID], nil
}

func (f *FakeDB) AddWorker(ctx context.Context, w internal.Worker, labels []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.OnAddWorker != nil {
		return f.OnAddWorker(w, labels)
	}
	for _, e := range f.Workers {
		if e.PodName == w.PodName {
			return internal.ErrDuplicatePodName
		}
	}
	f.Workers = append(f.Workers, w)
	f.WorkerStatus[w.PodName] = "pending"
	return nil
}

func (f *FakeDB) MarkWorkerRunning(ctx context.Context, pod, node string, _ *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MarkRunning = append(f.MarkRunning, pod)
	f.WorkerStatus[pod] = "running"
	return nil
}

func (f *FakeDB) MarkWorkerCompleted(ctx context.Context, pod, node string, _ *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MarkComplete = append(f.MarkComplete, pod)
	f.WorkerStatus[pod] = "completed"
	return nil
}

func (f *FakeDB) MarkWorkerFailed(ctx context.Context, pod, node string, info internal.FailureInfo, completedAt *time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MarkFailed = append(f.MarkFailed, FailCall{PodName: pod, Node: node, Info: info, CompletedAt: completedAt})
	f.WorkerStatus[pod] = "failed"
	return nil
}

func (f *FakeDB) MarkWorkerOrphaned(ctx context.Context, pod string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.MarkOrphaned = append(f.MarkOrphaned, pod)
	f.WorkerStatus[pod] = "completed"
	return nil
}

func (f *FakeDB) GetActiveJobsAndWorkers(ctx context.Context) ([]internal.Job, []internal.Worker, error) {
	if f.OnGetActiveJobsAndWorkers != nil {
		return f.OnGetActiveJobsAndWorkers()
	}
	return f.Jobs, f.Workers, nil
}

func (f *FakeDB) GetActiveWorkers(ctx context.Context) ([]internal.Worker, error) {
	return f.Workers, nil
}

func (f *FakeDB) GetAllWorkers(ctx context.Context, start, end string, page, perPage int) ([]internal.Worker, int, error) {
	if f.OnGetAllWorkers != nil {
		return f.OnGetAllWorkers(start, end, page, perPage)
	}
	return f.Workers, len(f.Workers), nil
}

func (f *FakeDB) GetWorkersForReconcile(ctx context.Context, terminal time.Duration) ([]internal.Worker, error) {
	if f.OnGetWorkersReconcile != nil {
		return f.OnGetWorkersReconcile(terminal)
	}
	out := make([]internal.Worker, 0, len(f.Workers))
	for _, w := range f.Workers {
		if st, ok := f.WorkerStatus[w.PodName]; ok {
			w.Status = st
		}
		out = append(out, w)
	}
	return out, nil
}

func (f *FakeDB) AddInstallationEvent(ctx context.Context, e internal.InstallationEvent, payload []byte) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Events = append(f.Events, EventCall{Row: e, Payload: payload})
	if f.OnAddEvent != nil {
		return f.OnAddEvent(e, payload)
	}
	return int64(len(f.Events)), nil
}

func (f *FakeDB) GetEventsByEntityID(ctx context.Context, entityID int64) ([]internal.InstallationEvent, error) {
	if f.OnGetEventsByEntityID != nil {
		return f.OnGetEventsByEntityID(entityID)
	}
	return nil, nil
}

func (f *FakeDB) GetPayloadByID(ctx context.Context, eventID int64) ([]byte, error) {
	if f.OnGetPayloadByID != nil {
		return f.OnGetPayloadByID(eventID)
	}
	return nil, nil
}

func (f *FakeDB) GetEntityIDForInstallation(ctx context.Context, id int64) (int64, bool, error) {
	if f.OnGetEntityIDInstall != nil {
		return f.OnGetEntityIDInstall(id)
	}
	return 0, false, nil
}

func (f *FakeDB) GetEntityIDForJob(ctx context.Context, id int64) (int64, bool, error) {
	if f.OnGetEntityIDJob != nil {
		return f.OnGetEntityIDJob(id)
	}
	return 0, false, nil
}

func (f *FakeDB) WithWorkerLock(ctx context.Context, fn func(context.Context) error) error {
	f.LockHeld = true
	defer func() { f.LockHeld = false }()
	return fn(ctx)
}

// --- FakeGH ---

// FakeGH satisfies internal.GitHubClient. Tests substitute the per-method
// callbacks; absent callbacks return zero values.
type FakeGH struct {
	OnAuthenticateApp     func(int64, int64) (string, error)
	OnGetInstallation     func(int64, internal.EntityType) (internal.Installation, error)
	OnEnsureRunnerGroup   func(string, string, string) (int64, error)
	OnCreateJITRunnerOrg  func(string, string, string, int64, []string) (string, error)
	OnCreateJITRunnerRepo func(string, string, string, []string) (string, error)
	OnListRunnersOrgGroup func(string, string, int64) ([]internal.GHRunner, error)
	OnListRunnersRepo     func(string, string) ([]internal.GHRunner, error)
	OnDeleteRunnerOrg     func(string, string, int64) error
	OnDeleteRunnerRepo    func(string, string, int64) error
	OnGetJobInfo          func(string, string, int64) (internal.GHJob, error)
	OnGetRunInfo          func(string, string, int64) (internal.GHRun, error)
}

func (g *FakeGH) AuthenticateApp(ctx context.Context, instID, appID int64) (string, error) {
	if g.OnAuthenticateApp != nil {
		return g.OnAuthenticateApp(instID, appID)
	}
	return "token", nil
}

func (g *FakeGH) GetInstallation(ctx context.Context, instID int64, et internal.EntityType) (internal.Installation, error) {
	if g.OnGetInstallation != nil {
		return g.OnGetInstallation(instID, et)
	}
	return internal.Installation{}, nil
}

func (g *FakeGH) EnsureRunnerGroup(ctx context.Context, token, org, group string) (int64, error) {
	if g.OnEnsureRunnerGroup != nil {
		return g.OnEnsureRunnerGroup(token, org, group)
	}
	return 0, nil
}

func (g *FakeGH) CreateJITRunnerConfigOrg(ctx context.Context, token, org, runner string, group int64, labels []string) (string, error) {
	if g.OnCreateJITRunnerOrg != nil {
		return g.OnCreateJITRunnerOrg(token, org, runner, group, labels)
	}
	return "jit", nil
}

func (g *FakeGH) CreateJITRunnerConfigRepo(ctx context.Context, token, repo, runner string, labels []string) (string, error) {
	if g.OnCreateJITRunnerRepo != nil {
		return g.OnCreateJITRunnerRepo(token, repo, runner, labels)
	}
	return "jit", nil
}

func (g *FakeGH) ListRunnersOrgGroup(ctx context.Context, token, org string, group int64) ([]internal.GHRunner, error) {
	if g.OnListRunnersOrgGroup != nil {
		return g.OnListRunnersOrgGroup(token, org, group)
	}
	return nil, nil
}

func (g *FakeGH) ListRunnersRepo(ctx context.Context, token, repo string) ([]internal.GHRunner, error) {
	if g.OnListRunnersRepo != nil {
		return g.OnListRunnersRepo(token, repo)
	}
	return nil, nil
}

func (g *FakeGH) DeleteRunnerOrg(ctx context.Context, token, org string, id int64) error {
	if g.OnDeleteRunnerOrg != nil {
		return g.OnDeleteRunnerOrg(token, org, id)
	}
	return nil
}

func (g *FakeGH) DeleteRunnerRepo(ctx context.Context, token, repo string, id int64) error {
	if g.OnDeleteRunnerRepo != nil {
		return g.OnDeleteRunnerRepo(token, repo, id)
	}
	return nil
}

func (g *FakeGH) GetJobInfo(ctx context.Context, token, repo string, jobID int64) (internal.GHJob, error) {
	if g.OnGetJobInfo != nil {
		return g.OnGetJobInfo(token, repo, jobID)
	}
	return internal.GHJob{}, nil
}

func (g *FakeGH) GetRunInfo(ctx context.Context, token, repo string, runID int64) (internal.GHRun, error) {
	if g.OnGetRunInfo != nil {
		return g.OnGetRunInfo(token, repo, runID)
	}
	return internal.GHRun{}, nil
}

// --- FakeKube ---

// FakeKube satisfies internal.KubeClient with in-memory state. Pods are
// addressable by name; the SlotsByPool map drives AvailableSlots.
type FakeKube struct {
	mu sync.Mutex

	PodsByName  map[string]internal.Pod
	NodesByName map[string]internal.Node
	EventsByPod map[string][]internal.PodEvent
	LogsByPod   map[string]string
	SlotsByPool map[string]int
	SlotCalls   map[string]int

	ProvisionCalls   []string
	DeleteCalls      []string
	ForceDeleteCalls []string
	KillCalls        []string

	OnProvisionRunner func(jit, name, image, pool string, entity internal.Entity) error
	OnGetPodEvents    func(podName string) ([]internal.PodEvent, error)
	OnListNodes       func() ([]internal.Node, error)
}

// NewFakeKube allocates the maps so callers can mutate them directly.
func NewFakeKube() *FakeKube {
	return &FakeKube{
		PodsByName:  map[string]internal.Pod{},
		NodesByName: map[string]internal.Node{},
		EventsByPod: map[string][]internal.PodEvent{},
		LogsByPod:   map[string]string{},
		SlotsByPool: map[string]int{},
		SlotCalls:   map[string]int{},
	}
}

func (f *FakeKube) ProvisionRunner(ctx context.Context, jit, name, image, pool string, entity internal.Entity) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ProvisionCalls = append(f.ProvisionCalls, name)
	if f.OnProvisionRunner != nil {
		return f.OnProvisionRunner(jit, name, image, pool, entity)
	}
	return nil
}

func (f *FakeKube) ListPods(ctx context.Context) ([]internal.Pod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]internal.Pod, 0, len(f.PodsByName))
	for _, p := range f.PodsByName {
		out = append(out, p)
	}
	return out, nil
}

func (f *FakeKube) GetPodEvents(ctx context.Context, podName string) ([]internal.PodEvent, error) {
	if f.OnGetPodEvents != nil {
		return f.OnGetPodEvents(podName)
	}
	return f.EventsByPod[podName], nil
}

func (f *FakeKube) GetPodLogs(ctx context.Context, podName, container string) (string, error) {
	return f.LogsByPod[podName], nil
}

func (f *FakeKube) DeletePod(ctx context.Context, podName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DeleteCalls = append(f.DeleteCalls, podName)
	delete(f.PodsByName, podName)
	return nil
}

func (f *FakeKube) ForceDeletePod(ctx context.Context, podName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ForceDeleteCalls = append(f.ForceDeleteCalls, podName)
	delete(f.PodsByName, podName)
	return nil
}

func (f *FakeKube) ListNodes(ctx context.Context) ([]internal.Node, error) {
	if f.OnListNodes != nil {
		return f.OnListNodes()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]internal.Node, 0, len(f.NodesByName))
	for _, n := range f.NodesByName {
		out = append(out, n)
	}
	return out, nil
}

func (f *FakeKube) KillPod(ctx context.Context, podName string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.KillCalls = append(f.KillCalls, podName)
	return nil
}

func (f *FakeKube) AvailableSlots(ctx context.Context, pool string) (internal.Capacity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SlotCalls[pool]++
	return internal.Capacity{Available: f.SlotsByPool[pool]}, nil
}

func (f *FakeKube) CollectPodFailureInfo(ctx context.Context, p internal.Pod, reason internal.FailureReason) internal.FailureInfoV2 {
	return internal.FailureInfoV2{Reason: reason}
}
