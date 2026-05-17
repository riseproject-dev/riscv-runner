// SPDX-License-Identifier: MIT

package internal

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/utils/ptr"
)

// K8sClient implements KubeClient against a real cluster (client-go).
type K8sClient struct {
	cs        kubernetes.Interface
	Namespace string // defaults to "default"
}

// NewK8sClient builds a client from a kubeconfig YAML string (K8S_KUBECONFIG).
func NewK8sClient(kubeconfigYAML string) (*K8sClient, error) {
	cfg, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfigYAML))
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("clientset: %w", err)
	}
	return &K8sClient{cs: cs, Namespace: "default"}, nil
}

// NewK8sClientFromInterface lets tests inject a fake clientset.
func NewK8sClientFromInterface(cs kubernetes.Interface) *K8sClient {
	return &K8sClient{cs: cs, Namespace: "default"}
}

// ProvisionRunner creates the runner pod. The exact shape (host-network,
// privileged, two emptyDir volumes, single container, RUNNER_JITCONFIG env,
// ephemeral-storage limit on scw-em-* only) is load-bearing. Don't tweak
// without a test.
func (k *K8sClient) ProvisionRunner(ctx context.Context, jitConfig, runnerName, image, pool string, entity Entity) error {
	limits := corev1.ResourceList{
		"riseproject.com/runner": resource.MustParse("1"),
	}
	if strings.HasPrefix(pool, "scw-em-") {
		limits["ephemeral-storage"] = resource.MustParse("90Gi")
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: runnerName,
			Labels: map[string]string{
				"app":                         "rise-riscv-runner",
				"riseproject.dev/entity_id":   strconv.FormatInt(entity.ID, 10),
				"riseproject.dev/entity_name": entity.Name,
				"riseproject.dev/board":       pool,
			},
		},
		Spec: corev1.PodSpec{
			NodeSelector: map[string]string{"riseproject.dev/board": pool},
			// 24h queue limit + 5d execution limit + 2h buffer = 525600s.
			ActiveDeadlineSeconds: ptr.To(int64(525600)),
			RestartPolicy:         corev1.RestartPolicyNever,
			HostNetwork:           true,
			Containers: []corev1.Container{{
				Name:            "runner",
				Image:           image,
				ImagePullPolicy: corev1.PullIfNotPresent,
				// privileged is required so the in-container dockerd can set
				// up iptables rules and the docker0 bridge.
				SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true)},
				Env: []corev1.EnvVar{
					{Name: "RUNNER_WAIT_FOR_DOCKER_IN_SECONDS", Value: "60"},
					{Name: "RUNNER_JITCONFIG", Value: jitConfig},
				},
				Resources: corev1.ResourceRequirements{Limits: limits},
				VolumeMounts: []corev1.VolumeMount{
					{Name: "docker-graph", MountPath: "/var/lib/docker"},
					{Name: "k0s", MountPath: "/var/lib/k0s"},
				},
			}},
			Volumes: []corev1.Volume{
				{Name: "docker-graph", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
				{Name: "k0s", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			},
		},
	}
	_, err := k.cs.CoreV1().Pods(k.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	return err
}

func (k *K8sClient) ListPods(ctx context.Context) ([]Pod, error) {
	list, err := k.cs.CoreV1().Pods(k.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=rise-riscv-runner",
	})
	if err != nil {
		return nil, err
	}
	out := make([]Pod, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, convertPod(&list.Items[i]))
	}
	return out, nil
}

// convertPod maps a corev1.Pod to our slimmer internal.Pod.
func convertPod(p *corev1.Pod) Pod {
	out := Pod{
		Name:         p.Name,
		Phase:        string(p.Status.Phase),
		NodeName:     p.Spec.NodeName,
		Message:      p.Status.Message,
		Reason:       p.Status.Reason,
		CreationTime: p.CreationTimestamp.Time,
	}
	for _, cs := range p.Status.ContainerStatuses {
		out.Containers = append(out.Containers, convertContainerStatus(cs))
	}
	for _, cs := range p.Status.InitContainerStatuses {
		out.InitContainers = append(out.InitContainers, convertContainerStatus(cs))
	}
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			t := c.LastTransitionTime.Time
			out.ReadyTransition = &t
			break
		}
	}
	return out
}

func convertContainerStatus(cs corev1.ContainerStatus) ContainerStatus {
	out := ContainerStatus{Name: cs.Name}
	if cs.State.Running != nil {
		out.Running = true
		t := cs.State.Running.StartedAt.Time
		out.RunningStarted = &t
	}
	if cs.State.Terminated != nil {
		out.Terminated = true
		t := cs.State.Terminated.FinishedAt.Time
		out.TerminatedAt = &t
		ec := cs.State.Terminated.ExitCode
		out.ExitCode = &ec
		out.Reason = cs.State.Terminated.Reason
		out.Message = cs.State.Terminated.Message
	}
	if cs.State.Waiting != nil {
		out.Waiting = true
		out.WaitingReason = cs.State.Waiting.Reason
		out.WaitingMessage = cs.State.Waiting.Message
	}
	return out
}

func (k *K8sClient) GetPodEvents(ctx context.Context, podName string) ([]PodEvent, error) {
	evs, err := k.cs.CoreV1().Events(k.Namespace).List(ctx, metav1.ListOptions{
		FieldSelector: "involvedObject.name=" + podName,
	})
	if err != nil {
		return nil, err
	}
	out := make([]PodEvent, 0, len(evs.Items))
	for _, e := range evs.Items {
		out = append(out, convertEvent(e))
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := eventTime(out[i]), eventTime(out[j])
		if ti == nil || tj == nil {
			return ti != nil
		}
		return ti.Before(*tj)
	})
	return out, nil
}

func eventTime(e PodEvent) *time.Time {
	if e.LastSeen != nil {
		return e.LastSeen
	}
	if e.FirstSeen != nil {
		return e.FirstSeen
	}
	return nil
}

func convertEvent(e corev1.Event) PodEvent {
	out := PodEvent{Type: e.Type, Reason: e.Reason, Message: e.Message, Count: e.Count}
	if !e.FirstTimestamp.IsZero() {
		t := e.FirstTimestamp.Time
		out.FirstSeen = &t
	}
	if !e.LastTimestamp.IsZero() {
		t := e.LastTimestamp.Time
		out.LastSeen = &t
	} else if e.EventTime.Time != (time.Time{}) {
		t := e.EventTime.Time
		out.LastSeen = &t
	}
	return out
}

func (k *K8sClient) GetPodLogs(ctx context.Context, podName, container string) (string, error) {
	req := k.cs.CoreV1().Pods(k.Namespace).GetLogs(podName, &corev1.PodLogOptions{Container: container})
	rc, err := req.Stream(ctx)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, err := rc.Read(tmp)
		if n > 0 {
			buf = append(buf, tmp[:n]...)
		}
		if err != nil {
			break
		}
	}
	return string(buf), nil
}

// DeletePod treats a 404 as success (pod already gone).
func (k *K8sClient) DeletePod(ctx context.Context, podName string) error {
	err := k.cs.CoreV1().Pods(k.Namespace).Delete(ctx, podName, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// KillPod patches activeDeadlineSeconds=1 so the kubelet marks the pod Failed
// (DeadlineExceeded) without removing it from the cluster — logs and events
// stay inspectable until Phase 5 deletes the pod. 404 is treated as success.
func (k *K8sClient) KillPod(ctx context.Context, podName string) error {
	patch := []byte(`{"spec":{"activeDeadlineSeconds":1}}`)
	_, err := k.cs.CoreV1().Pods(k.Namespace).Patch(ctx, podName, "application/strategic-merge-patch+json", patch, metav1.PatchOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

// AvailableSlots returns total allocatable runner capacity on the pool's
// nodes and the count of currently active runner pods. The pool → node-label
// mapping is k8s-internal; callers stay in pool-name space.
func (k *K8sClient) AvailableSlots(ctx context.Context, pool string) (Capacity, error) {
	labelSelector := "riseproject.dev/board=" + pool
	nodes, err := k.cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return Capacity{}, err
	}
	var total int
	for _, n := range nodes.Items {
		if q, ok := n.Status.Allocatable["riseproject.com/runner"]; ok {
			total += int(q.Value())
		}
	}
	pods, err := k.cs.CoreV1().Pods(k.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app=rise-riscv-runner," + labelSelector,
	})
	if err != nil {
		return Capacity{}, err
	}
	active := 0
	for _, p := range pods.Items {
		switch p.Status.Phase {
		case corev1.PodPending, corev1.PodRunning:
			active++
		}
	}
	return Capacity{Total: total, Active: active, Available: total - active}, nil
}

// CollectPodFailureInfo builds the FailureInfoV2 shape: container exit
// codes/messages/logs plus pod events. Best-effort — never returns an error.
func (k *K8sClient) CollectPodFailureInfo(ctx context.Context, p Pod, reason FailureReason) FailureInfoV2 {
	info := FailureInfoV2{
		Reason:     reason,
		Containers: map[string]ContainerInfo{},
		Events:     nil,
		PodMessage: p.Message,
		PodReason:  p.Reason,
	}
	collect := func(list []ContainerStatus) {
		for _, cs := range list {
			ci := ContainerInfo{
				ExitCode: cs.ExitCode,
				Reason:   cs.Reason,
				Message:  cs.Message,
			}
			if cs.Waiting && ci.Reason == "" && ci.Message == "" {
				ci.Reason = cs.WaitingReason
				ci.Message = cs.WaitingMessage
			}
			logs, _ := k.GetPodLogs(ctx, p.Name, cs.Name)
			ci.Logs = logs
			info.Containers[cs.Name] = ci
		}
	}
	collect(p.Containers)
	collect(p.InitContainers)
	evs, _ := k.GetPodEvents(ctx, p.Name) // best-effort; an empty Events slice is OK
	for _, ev := range evs {
		info.Events = append(info.Events, EventInfo{
			Type:      ev.Type,
			Reason:    ev.Reason,
			Message:   ev.Message,
			Count:     ev.Count,
			FirstSeen: optTime(ev.FirstSeen),
			LastSeen:  optTime(ev.LastSeen),
		})
	}
	return info
}

func optTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(time.RFC3339)
}
