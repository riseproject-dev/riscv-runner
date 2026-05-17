package labeler

import (
	"context"
	"encoding/json"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const labelKey = "riseproject.dev/board"

// LabelNode patches the given node with the riseproject.dev/board label.
func LabelNode(nodeName, board string) error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	patch := map[string]interface{}{
		"metadata": map[string]interface{}{
			"labels": map[string]string{
				labelKey: board,
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("failed to marshal patch: %w", err)
	}

	_, err = clientset.CoreV1().Nodes().Patch(
		context.TODO(),
		nodeName,
		types.MergePatchType,
		patchBytes,
		metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("failed to patch node %s: %w", nodeName, err)
	}

	klog.Infof("Labeled node %s with %s=%s", nodeName, labelKey, board)
	return nil
}
