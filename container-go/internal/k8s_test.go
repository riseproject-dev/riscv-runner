package internal

import (
	"context"
	"strings"
	"testing"

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
	for _, pool := range []string{"scw-em-rv1", "cloudv10x-jupiter"} {
		k := NewK8sClientFromInterface(fake.NewSimpleClientset())
		if err := k.ProvisionRunner(context.Background(), "jit", "runner-"+pool, "img", pool, Entity{ID: 1, Name: "ent"}); err != nil {
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
	if err := k.ProvisionRunner(context.Background(), "jit", "r", "img", "scw-em-rv1", Entity{ID: 1, Name: "ent"}); err != nil {
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

// TestProvisionRunner_DiskLimitsOnlyOnScwEM asserts ephemeral-storage=90Gi
// only on scw-em-* pools (invariant 3286cf6).
func TestProvisionRunner_DiskLimitsOnlyOnScwEM(t *testing.T) {
	tests := []struct {
		pool     string
		wantDisk bool
	}{
		{"scw-em-rv1", true},
		{"scw-em-something", true},
		{"cloudv10x-jupiter", false},
	}
	for _, tc := range tests {
		k := NewK8sClientFromInterface(fake.NewSimpleClientset())
		if err := k.ProvisionRunner(context.Background(), "jit", "r-"+tc.pool, "img", tc.pool, Entity{ID: 1, Name: "ent"}); err != nil {
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
	if err := k.ProvisionRunner(context.Background(), "jit", "r", "img", "scw-em-rv1", Entity{ID: 1, Name: "ent"}); err != nil {
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
	if err := k.ProvisionRunner(context.Background(), "jit", "r", "img", "scw-em-rv1", Entity{ID: 42, Name: "pytorch"}); err != nil {
		t.Fatalf("provision: %v", err)
	}
	p := fakePod(t, k, "r")
	want := map[string]string{
		"app":                         "rise-riscv-runner",
		"riseproject.dev/entity_id":   "42",
		"riseproject.dev/entity_name": "pytorch",
		"riseproject.dev/board":       "scw-em-rv1",
	}
	for k, v := range want {
		if p.Labels[k] != v {
			t.Errorf("label %s=%q want %q", k, p.Labels[k], v)
		}
	}
	if p.Spec.NodeSelector["riseproject.dev/board"] != "scw-em-rv1" {
		t.Errorf("nodeSelector board mismatch: %v", p.Spec.NodeSelector)
	}
}

// TestProvisionRunner_TimeoutsAndPrivileged asserts the lesser invariants:
// activeDeadlineSeconds=525600, restartPolicy=Never, container privileged=true.
func TestProvisionRunner_TimeoutsAndPrivileged(t *testing.T) {
	k := NewK8sClientFromInterface(fake.NewSimpleClientset())
	if err := k.ProvisionRunner(context.Background(), "jit", "r", "img", "scw-em-rv1", Entity{ID: 1, Name: "ent"}); err != nil {
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
