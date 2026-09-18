// SPDX-License-Identifier: MIT

package internal

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// fakePod returns the pod the fake clientset stored under runnerName.
func fakePod(t *testing.T, k *K8sClient, runnerName string) *corev1.Pod {
	t.Helper()
	pod, err := k.cs.CoreV1().Pods("default").Get(context.Background(), runnerName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get pod: %v", err)
	}
	return pod
}

// TestProvisionRunner_UsesHostNetwork asserts pod.spec.hostNetwork=true on every
// pool — invariant 9de4c35.
func TestProvisionRunner_UsesHostNetwork(t *testing.T) {
	for _, pool := range []string{"scaleway-em-rv1", "spacemit-k1"} {
		k := NewK8sClientFromInterface(fake.NewSimpleClientset())
		if err := k.ProvisionRunner(context.Background(), "jit", "runner-"+pool, "img", SelectorForBoard(pool), Entity{ID: 1, Name: "ent"}); err != nil {
			t.Fatalf("provision: %v", err)
		}
		p := fakePod(t, k, "runner-"+pool)
		if !p.Spec.HostNetwork {
			t.Errorf("pool=%s expected HostNetwork=true", pool)
		}
	}
}

// TestProvisionRunner_EmptyDirVolumes asserts the two emptyDir volumes for
// /var/lib/docker and /var/lib/k0s exist on every pool (invariants 0028278/653a5ba).
func TestProvisionRunner_EmptyDirVolumes(t *testing.T) {
	k := NewK8sClientFromInterface(fake.NewSimpleClientset())
	if err := k.ProvisionRunner(context.Background(), "jit", "r", "img", SelectorForBoard("scaleway-em-rv1"), Entity{ID: 1, Name: "ent"}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	p := fakePod(t, k, "r")
	type mount struct {
		name string
		path string
	}
	want := []mount{{"docker-graph", "/var/lib/docker"}, {"k0s", "/var/lib/k0s"}}
	if len(p.Spec.Containers) != 1 {
		t.Fatalf("expected single container, got %d", len(p.Spec.Containers))
	}
	for _, m := range want {
		var foundVolume, foundMount bool
		for _, v := range p.Spec.Volumes {
			if v.Name == m.name && v.EmptyDir != nil {
				foundVolume = true
			}
		}
		for _, vm := range p.Spec.Containers[0].VolumeMounts {
			if vm.Name == m.name && vm.MountPath == m.path {
				foundMount = true
			}
		}
		if !foundVolume {
			t.Errorf("volume %s emptyDir not found", m.name)
		}
		if !foundMount {
			t.Errorf("volumeMount %s at %s not found", m.name, m.path)
		}
	}
}

// TestProvisionRunner_DiskLimitsOnlyOnScalewayEM asserts ephemeral-storage=90Gi
// only on scaleway-em-* pools (invariant 3286cf6).
func TestProvisionRunner_DiskLimitsOnlyOnScalewayEM(t *testing.T) {
	tests := []struct {
		pool     string
		wantDisk bool
	}{
		{"scaleway-em-rv1", true},
		{"scaleway-em-something", true},
		{"spacemit-k1", false},
	}
	for _, tc := range tests {
		k := NewK8sClientFromInterface(fake.NewSimpleClientset())
		if err := k.ProvisionRunner(context.Background(), "jit", "r-"+tc.pool, "img", SelectorForBoard(tc.pool), Entity{ID: 1, Name: "ent"}); err != nil {
			t.Fatalf("[%s] provision: %v", tc.pool, err)
		}
		p := fakePod(t, k, "r-"+tc.pool)
		limits := p.Spec.Containers[0].Resources.Limits
		_, has := limits["ephemeral-storage"]
		if has != tc.wantDisk {
			t.Errorf("pool=%s ephemeral-storage present=%v want=%v", tc.pool, has, tc.wantDisk)
		}
		if has {
			q := limits["ephemeral-storage"]
			want := resource.MustParse("90Gi")
			if q.Cmp(want) != 0 {
				t.Errorf("pool=%s ephemeral-storage=%s want 90Gi", tc.pool, q.String())
			}
		}
		if _, has := limits["riseproject.com/runner"]; !has {
			t.Errorf("pool=%s runner limit missing", tc.pool)
		}
	}
}

// TestProvisionRunner_NoSidecar asserts pod has exactly one container, no
// docker-certs volume, no DOCKER_* env (invariant 5c5004f).
func TestProvisionRunner_NoSidecar(t *testing.T) {
	k := NewK8sClientFromInterface(fake.NewSimpleClientset())
	if err := k.ProvisionRunner(context.Background(), "jit", "r", "img", SelectorForBoard("scaleway-em-rv1"), Entity{ID: 1, Name: "ent"}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	p := fakePod(t, k, "r")
	if len(p.Spec.Containers) != 1 {
		t.Fatalf("expected single container, got %d", len(p.Spec.Containers))
	}
	for _, v := range p.Spec.Volumes {
		if strings.Contains(v.Name, "docker-cert") {
			t.Errorf("docker-certs volume %s leaked into spec", v.Name)
		}
	}
	for _, e := range p.Spec.Containers[0].Env {
		if strings.HasPrefix(e.Name, "DOCKER_") {
			t.Errorf("DOCKER_* env leaked: %s", e.Name)
		}
	}
	// Required env present
	mustHaveEnv(t, p.Spec.Containers[0].Env, "RUNNER_WAIT_FOR_DOCKER_IN_SECONDS", "60")
	mustHaveEnv(t, p.Spec.Containers[0].Env, "RUNNER_JITCONFIG", "jit")
}

func mustHaveEnv(t *testing.T, env []corev1.EnvVar, name, value string) {
	t.Helper()
	for _, e := range env {
		if e.Name == name {
			if e.Value != value {
				t.Errorf("env %s=%q want %q", name, e.Value, value)
			}
			return
		}
	}
	t.Errorf("env %s missing", name)
}

// TestProvisionRunner_Labels asserts the four pod labels are set.
func TestProvisionRunner_Labels(t *testing.T) {
	k := NewK8sClientFromInterface(fake.NewSimpleClientset())
	if err := k.ProvisionRunner(context.Background(), "jit", "r", "img", SelectorForBoard("scaleway-em-rv1"), Entity{ID: 42, Name: "pytorch"}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	p := fakePod(t, k, "r")
	want := map[string]string{
		"app":                         "rise-riscv-runner",
		"riseproject.dev/entity_id":   "42",
		"riseproject.dev/entity_name": "pytorch",
		"riseproject.dev/board":       "scaleway-em-rv1",
	}
	for k, v := range want {
		if p.Labels[k] != v {
			t.Errorf("label %s=%q want %q", k, p.Labels[k], v)
		}
	}
	if p.Spec.NodeSelector["riseproject.dev/board"] != "scaleway-em-rv1" {
		t.Errorf("nodeSelector board mismatch: %v", p.Spec.NodeSelector)
	}
}

// TestProvisionRunner_TimeoutsAndPrivileged asserts the lesser invariants:
// activeDeadlineSeconds=525600, restartPolicy=Never, container privileged=true.
func TestProvisionRunner_TimeoutsAndPrivileged(t *testing.T) {
	k := NewK8sClientFromInterface(fake.NewSimpleClientset())
	if err := k.ProvisionRunner(context.Background(), "jit", "r", "img", SelectorForBoard("scaleway-em-rv1"), Entity{ID: 1, Name: "ent"}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	p := fakePod(t, k, "r")
	if p.Spec.ActiveDeadlineSeconds == nil || *p.Spec.ActiveDeadlineSeconds != 525600 {
		t.Errorf("activeDeadlineSeconds=%v want 525600", p.Spec.ActiveDeadlineSeconds)
	}
	if p.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy=%v want Never", p.Spec.RestartPolicy)
	}
	sc := p.Spec.Containers[0].SecurityContext
	if sc == nil || sc.Privileged == nil || !*sc.Privileged {
		t.Errorf("container not privileged")
	}
}

// makeNode returns a fake node with allocatable runner capacity, labelled from
// the given selector the way a real node is (board by the device plugin,
// provider by hand).
func makeNode(name string, sel NodeSelector, capacity int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: sel.Labels(),
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				"riseproject.com/runner": *resource.NewQuantity(capacity, resource.DecimalSI),
			},
		},
	}
}

// makePod returns a fake runner pod carrying the selector's labels, mirroring
// what ProvisionRunner stamps on real pods. nodeName is the node it landed on;
// "" means not yet scheduled, which only a Pending pod can be.
func makePod(name string, sel NodeSelector, nodeName string, phase corev1.PodPhase) *corev1.Pod {
	labels := map[string]string{"app": "rise-riscv-runner"}
	for k, v := range sel.Labels() {
		labels[k] = v
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    labels,
		},
		Spec:   corev1.PodSpec{NodeName: nodeName},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func TestAvailableSlots_TotalMinusActive(t *testing.T) {
	scw := SelectorForBoard(BoardScalewayEMRV1)
	cs := fake.NewSimpleClientset(
		makeNode("n1", scw, 3),
		makeNode("n2", scw, 2),
		makeNode("other", SelectorForBoard(BoardSpacemitK1), 5), // different board, ignored
		makePod("p1", scw, "n1", corev1.PodPending),
		makePod("p2", scw, "n2", corev1.PodRunning),
		makePod("p3", scw, "n1", corev1.PodSucceeded), // terminal, doesn't count
		makePod("po", SelectorForBoard(BoardSpacemitK1), "other", corev1.PodRunning),
	)
	k := NewK8sClientFromInterface(cs)
	cap, err := k.AvailableSlots(context.Background(), SelectorForBoard("scaleway-em-rv1"))
	if err != nil {
		t.Fatalf("AvailableSlots: %v", err)
	}
	if cap.Total != 5 || cap.Active != 2 || cap.Available != 3 {
		t.Errorf("got %+v, want {Total:5 Active:2 Available:3}", cap)
	}
}

// TestAvailableSlots_MeasuredPerBoard pins the transitional behaviour:
// capacity is summed across the whole board, ignoring the selector's provider,
// so board-only pods from before the provider label are still counted. Each
// board model has one provider today, so this matches reality. Flip this test
// to per-provider expectations when AvailableSlots narrows to sel.Key().
func TestAvailableSlots_MeasuredPerBoard(t *testing.T) {
	cloudv := NodeSelector{Board: BoardSpacemitK1, Provider: ProviderCloudV10x}
	mengzhuo := NodeSelector{Board: BoardSpacemitK1, Provider: ProviderMengZhuo}
	cs := fake.NewSimpleClientset(
		makeNode("c1", cloudv, 4),
		makeNode("m1", mengzhuo, 1),
		makePod("cp1", cloudv, "c1", corev1.PodRunning),
	)
	k := NewK8sClientFromInterface(cs)

	// Both selectors see the board total (4+1) minus the one running pod.
	want := Capacity{Total: 5, Active: 1, Available: 4}
	for _, sel := range []NodeSelector{mengzhuo, cloudv} {
		got, err := k.AvailableSlots(context.Background(), sel)
		if err != nil {
			t.Fatalf("AvailableSlots(%+v): %v", sel, err)
		}
		if got != want {
			t.Errorf("%+v: got %+v, want %+v", sel, got, want)
		}
	}
}

// Another board's capacity and pods stay excluded: only the provider dimension
// is ignored, not the board.
func TestAvailableSlots_OtherBoardExcluded(t *testing.T) {
	k1 := NodeSelector{Board: BoardSpacemitK1, Provider: ProviderCloudV10x}
	k3 := NodeSelector{Board: BoardSpacemitK3, Provider: ProviderISCAS}
	cs := fake.NewSimpleClientset(
		makeNode("k1n", k1, 4),
		makeNode("k3n", k3, 2),
		makePod("k3p", k3, "k3n", corev1.PodRunning),
	)
	k := NewK8sClientFromInterface(cs)

	got, err := k.AvailableSlots(context.Background(), k1)
	if err != nil {
		t.Fatalf("AvailableSlots: %v", err)
	}
	if want := (Capacity{Total: 4, Active: 0, Available: 4}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestEmptySelectorRejected: an empty selector matches every node, so both
// entry points must refuse it rather than place a runner anywhere.
func TestEmptySelectorRejected(t *testing.T) {
	k := NewK8sClientFromInterface(fake.NewSimpleClientset())
	if _, err := k.AvailableSlots(context.Background(), NodeSelector{}); !errors.Is(err, ErrEmptySelector) {
		t.Errorf("AvailableSlots(nil) err=%v, want ErrEmptySelector", err)
	}
	if err := k.ProvisionRunner(context.Background(), "jit", "r", "img", NodeSelector{}, Entity{ID: 1}); !errors.Is(err, ErrEmptySelector) {
		t.Errorf("ProvisionRunner(empty) err=%v, want ErrEmptySelector", err)
	}
}

func TestAvailableSlots_NoMatchingNodes(t *testing.T) {
	k := NewK8sClientFromInterface(fake.NewSimpleClientset())
	cap, err := k.AvailableSlots(context.Background(), SelectorForBoard("scaleway-em-rv1"))
	if err != nil {
		t.Fatalf("AvailableSlots: %v", err)
	}
	if cap.Total != 0 || cap.Active != 0 || cap.Available != 0 {
		t.Errorf("got %+v, want zero", cap)
	}
}

func TestListPods_FiltersByAppLabel(t *testing.T) {
	cs := fake.NewSimpleClientset(
		makePod("r1", SelectorForBoard(BoardScalewayEMRV1), "n1", corev1.PodRunning),
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "noise", Namespace: "default",
				Labels: map[string]string{"app": "something-else"},
			},
		},
	)
	k := NewK8sClientFromInterface(cs)
	pods, err := k.ListPods(context.Background())
	if err != nil {
		t.Fatalf("ListPods: %v", err)
	}
	if len(pods) != 1 || pods[0].Name != "r1" {
		t.Errorf("filter failed: %+v", pods)
	}
}

func TestDeletePod_404IsSilentSuccess(t *testing.T) {
	cs := fake.NewSimpleClientset()
	k := NewK8sClientFromInterface(cs)
	if err := k.DeletePod(context.Background(), "nope"); err != nil {
		t.Fatalf("expected nil on 404, got %v", err)
	}
}

func TestDeletePod_Deletes(t *testing.T) {
	cs := fake.NewSimpleClientset(makePod("p", SelectorForBoard(BoardScalewayEMRV1), "n1", corev1.PodSucceeded))
	k := NewK8sClientFromInterface(cs)
	if err := k.DeletePod(context.Background(), "p"); err != nil {
		t.Fatalf("DeletePod: %v", err)
	}
	if _, err := cs.CoreV1().Pods("default").Get(context.Background(), "p", metav1.GetOptions{}); err == nil {
		t.Fatalf("pod should be gone")
	}
}

func TestKillPod_PatchesActiveDeadlineSeconds(t *testing.T) {
	cs := fake.NewSimpleClientset(makePod("p", SelectorForBoard(BoardScalewayEMRV1), "n1", corev1.PodRunning))
	k := NewK8sClientFromInterface(cs)
	if err := k.KillPod(context.Background(), "p"); err != nil {
		t.Fatalf("KillPod: %v", err)
	}
	got, err := cs.CoreV1().Pods("default").Get(context.Background(), "p", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Spec.ActiveDeadlineSeconds == nil || *got.Spec.ActiveDeadlineSeconds != 1 {
		t.Errorf("activeDeadlineSeconds=%v want 1", got.Spec.ActiveDeadlineSeconds)
	}
}

func TestKillPod_404IsSilentSuccess(t *testing.T) {
	cs := fake.NewSimpleClientset()
	k := NewK8sClientFromInterface(cs)
	if err := k.KillPod(context.Background(), "nope"); err != nil {
		t.Fatalf("expected nil on 404, got %v", err)
	}
}

func TestGetPodEvents_SortedAscending(t *testing.T) {
	t1 := time.Now().Add(-10 * time.Minute)
	t2 := t1.Add(5 * time.Minute)
	// fake clientset doesn't honour the FieldSelector — only seed events
	// that involve the target pod so we exercise the sort path cleanly.
	cs := fake.NewSimpleClientset(
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e2", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Name: "p"},
			Type:           "Warning",
			Reason:         "Late",
			LastTimestamp:  metav1.Time{Time: t2},
		},
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Name: "p"},
			Type:           "Normal",
			Reason:         "Early",
			LastTimestamp:  metav1.Time{Time: t1},
		},
	)
	k := NewK8sClientFromInterface(cs)
	evs, err := k.GetPodEvents(context.Background(), "p")
	if err != nil {
		t.Fatalf("GetPodEvents: %v", err)
	}
	if len(evs) != 2 {
		t.Fatalf("len=%d want 2", len(evs))
	}
	if evs[0].Reason != "Early" || evs[1].Reason != "Late" {
		t.Errorf("not sorted ascending: %v", evs)
	}
}

func TestGetPodEvents_FallsBackToEventTime(t *testing.T) {
	t1 := time.Now().Add(-1 * time.Minute)
	cs := fake.NewSimpleClientset(
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Name: "p"},
			EventTime:      metav1.MicroTime{Time: t1},
			Type:           "Normal",
			Reason:         "X",
		},
	)
	k := NewK8sClientFromInterface(cs)
	evs, err := k.GetPodEvents(context.Background(), "p")
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].LastSeen == nil {
		t.Fatalf("expected EventTime to become LastSeen: %+v", evs)
	}
}

func TestConvertPod_CapturesContainerStates(t *testing.T) {
	now := metav1.Now()
	finished := metav1.NewTime(now.Time.Add(1 * time.Minute))
	exit := int32(137)
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "default"},
		Spec:       corev1.PodSpec{NodeName: "node-1"},
		Status: corev1.PodStatus{
			Phase:   corev1.PodFailed,
			Message: "container failed",
			Reason:  "OOMKilled",
			ContainerStatuses: []corev1.ContainerStatus{{
				Name: "runner",
				State: corev1.ContainerState{
					Terminated: &corev1.ContainerStateTerminated{
						ExitCode:   exit,
						Reason:     "OOMKilled",
						Message:    "out of memory",
						FinishedAt: finished,
					},
				},
			}, {
				Name: "init",
				State: corev1.ContainerState{
					Waiting: &corev1.ContainerStateWaiting{
						Reason:  "ImagePullBackOff",
						Message: "backoff",
					},
				},
			}, {
				Name: "running",
				State: corev1.ContainerState{
					Running: &corev1.ContainerStateRunning{StartedAt: now},
				},
			}},
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodReady, Status: corev1.ConditionTrue,
				LastTransitionTime: now,
			}},
		},
	}
	out := convertPod(p)
	if out.Phase != "Failed" || out.NodeName != "node-1" || out.Message != "container failed" {
		t.Errorf("pod fields wrong: %+v", out)
	}
	if out.ReadyTransition == nil {
		t.Errorf("ReadyTransition not captured")
	}
	if len(out.Containers) != 3 {
		t.Fatalf("expected 3 containers, got %d", len(out.Containers))
	}
	for _, cs := range out.Containers {
		switch cs.Name {
		case "runner":
			if !cs.Terminated || cs.ExitCode == nil || *cs.ExitCode != 137 || cs.Reason != "OOMKilled" {
				t.Errorf("runner state wrong: %+v", cs)
			}
		case "init":
			if !cs.Waiting || cs.WaitingReason != "ImagePullBackOff" {
				t.Errorf("init state wrong: %+v", cs)
			}
		case "running":
			if !cs.Running || cs.RunningStarted == nil {
				t.Errorf("running state wrong: %+v", cs)
			}
		}
	}
}

func TestPod_FinishedAtReturnsLatest(t *testing.T) {
	t1 := time.Now().Add(-5 * time.Minute)
	t2 := t1.Add(2 * time.Minute) // later
	p := Pod{Containers: []ContainerStatus{
		{Name: "a", Terminated: true, TerminatedAt: &t1},
		{Name: "b", Terminated: true, TerminatedAt: &t2},
	}}
	got := p.FinishedAt()
	if got == nil || !got.Equal(t2) {
		t.Errorf("FinishedAt=%v want %v", got, t2)
	}
}

func TestPod_FinishedAtNilWhenNotTerminated(t *testing.T) {
	p := Pod{Containers: []ContainerStatus{{Name: "a", Running: true}}}
	if p.FinishedAt() != nil {
		t.Errorf("expected nil")
	}
}

func TestPod_RunnerStartedAtFromRunner(t *testing.T) {
	start := time.Now().Add(-1 * time.Minute)
	p := Pod{Containers: []ContainerStatus{
		{Name: "init", Running: false},
		{Name: "runner", Running: true, RunningStarted: &start},
	}}
	got := p.RunnerStartedAt()
	if got == nil || !got.Equal(start) {
		t.Errorf("RunnerStartedAt=%v want %v", got, start)
	}
}

func TestPod_RunnerStartedAtFallsBackToReady(t *testing.T) {
	ready := time.Now()
	p := Pod{ReadyTransition: &ready}
	got := p.RunnerStartedAt()
	if got == nil || !got.Equal(ready) {
		t.Errorf("RunnerStartedAt=%v want %v", got, ready)
	}
}

func TestCollectPodFailureInfo_BuildsV2Shape(t *testing.T) {
	exit := int32(1)
	terminatedAt := time.Now()
	pod := Pod{
		Name: "p", Message: "msg", Reason: "BadStuff",
		Containers: []ContainerStatus{{
			Name: "runner", Terminated: true, TerminatedAt: &terminatedAt,
			ExitCode: &exit, Reason: "Error", Message: "boom",
		}, {
			Name: "waiting", Waiting: true,
			WaitingReason: "ImagePull", WaitingMessage: "pulling",
		}},
	}
	cs := fake.NewSimpleClientset(
		&corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: "e1", Namespace: "default"},
			InvolvedObject: corev1.ObjectReference{Name: "p"},
			Type:           "Warning", Reason: "Failed", Message: "uh-oh", Count: 1,
		},
	)
	k := NewK8sClientFromInterface(cs)
	info := k.CollectPodFailureInfo(context.Background(), pod, ReasonPodFailed)
	if info.Reason != ReasonPodFailed {
		t.Errorf("reason wrong: %+v", info)
	}
	if info.PodMessage != "msg" || info.PodReason != "BadStuff" {
		t.Errorf("pod fields lost: %+v", info)
	}
	runner, ok := info.Containers["runner"]
	if !ok || runner.ExitCode == nil || *runner.ExitCode != 1 || runner.Reason != "Error" {
		t.Errorf("runner container wrong: %+v", runner)
	}
	waiting := info.Containers["waiting"]
	if waiting.Reason != "ImagePull" || waiting.Message != "pulling" {
		t.Errorf("waiting container falls back to waiting fields: %+v", waiting)
	}
	if len(info.Events) != 1 || info.Events[0].Reason != "Failed" {
		t.Errorf("events not collected: %+v", info.Events)
	}
}

func TestEventTime_PrefersLastOverFirst(t *testing.T) {
	t1 := time.Now()
	t2 := t1.Add(time.Minute)
	ev := PodEvent{FirstSeen: &t1, LastSeen: &t2}
	got := eventTime(ev)
	if got == nil || !got.Equal(t2) {
		t.Errorf("got %v want %v", got, t2)
	}
}

func TestEventTime_FirstWhenLastNil(t *testing.T) {
	t1 := time.Now()
	ev := PodEvent{FirstSeen: &t1}
	got := eventTime(ev)
	if got == nil || !got.Equal(t1) {
		t.Errorf("got %v want %v", got, t1)
	}
}

func TestEventTime_NilWhenBothMissing(t *testing.T) {
	if eventTime(PodEvent{}) != nil {
		t.Error("expected nil")
	}
}

// legacyPod is a runner pod as the pre-selector binary created it: board label
// only, no riseproject.dev/provider.
func legacyPod(name, board, nodeName string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{"app": "rise-riscv-runner", "riseproject.dev/board": board},
		},
		Spec:   corev1.PodSpec{NodeName: nodeName},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// TestAvailableSlots_CountsLegacyPods: runners started before the provider
// label existed still occupy their node, so they must count as active or the
// scheduler will try to double-book that node.
func TestAvailableSlots_CountsLegacyPods(t *testing.T) {
	sel := SelectorForBoard(BoardSpacemitK3)
	cs := fake.NewSimpleClientset(
		makeNode("n1", sel, 1),
		legacyPod("old-runner", BoardSpacemitK3, "n1", corev1.PodRunning),
	)
	k := NewK8sClientFromInterface(cs)

	got, err := k.AvailableSlots(context.Background(), sel)
	if err != nil {
		t.Fatalf("AvailableSlots: %v", err)
	}
	if got.Total != 1 || got.Active != 1 || got.Available != 0 {
		t.Errorf("got %+v, want {Total:1 Active:1 Available:0}", got)
	}
}

// A legacy pod on another pool's node must not be counted here.
func TestAvailableSlots_LegacyPodOnOtherPoolIgnored(t *testing.T) {
	k3 := SelectorForBoard(BoardSpacemitK3)
	cs := fake.NewSimpleClientset(
		makeNode("k3-node", k3, 1),
		makeNode("k1-node", SelectorForBoard(BoardSpacemitK1), 1),
		legacyPod("old-runner", BoardSpacemitK1, "k1-node", corev1.PodRunning),
	)
	k := NewK8sClientFromInterface(cs)

	got, err := k.AvailableSlots(context.Background(), k3)
	if err != nil {
		t.Fatalf("AvailableSlots: %v", err)
	}
	if got.Total != 1 || got.Active != 0 || got.Available != 1 {
		t.Errorf("got %+v, want {Total:1 Active:0 Available:1}", got)
	}
}

// A board-only pod that has not been scheduled yet has no node to attribute it
// to, so it must be counted via its labels despite lacking the provider label.
func TestAvailableSlots_CountsUnscheduledLegacyPod(t *testing.T) {
	sel := SelectorForBoard(BoardSpacemitK3)
	cs := fake.NewSimpleClientset(
		makeNode("n1", sel, 1),
		legacyPod("old-pending", BoardSpacemitK3, "", corev1.PodPending),
	)
	k := NewK8sClientFromInterface(cs)

	got, err := k.AvailableSlots(context.Background(), sel)
	if err != nil {
		t.Fatalf("AvailableSlots: %v", err)
	}
	if want := (Capacity{Total: 1, Active: 1, Available: 0}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// An unscheduled pod has no node yet, so it is counted via its labels.
func TestAvailableSlots_CountsUnscheduledPod(t *testing.T) {
	sel := SelectorForBoard(BoardSpacemitK3)
	cs := fake.NewSimpleClientset(makeNode("n1", sel, 1), makePod("new", sel, "", corev1.PodPending))
	k := NewK8sClientFromInterface(cs)

	got, err := k.AvailableSlots(context.Background(), sel)
	if err != nil {
		t.Fatalf("AvailableSlots: %v", err)
	}
	if got.Active != 1 || got.Available != 0 {
		t.Errorf("got %+v, want Active:1 Available:0", got)
	}
}
