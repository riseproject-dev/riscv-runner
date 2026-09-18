// SPDX-License-Identifier: MIT

package internal

import (
	"bytes"
	"log/slog"
	"maps"
	"strings"
	"testing"
)

func TestNodeSelectorKey(t *testing.T) {
	tests := []struct {
		sel  NodeSelector
		want string
	}{
		{NodeSelector{Board: BoardSpacemitK3, Provider: ProviderISCAS},
			"riseproject.dev/board=spacemit-k3,riseproject.dev/provider=iscas"},
		// No provider means any vendor's machine of that board.
		{NodeSelector{Board: BoardSpacemitK3}, "riseproject.dev/board=spacemit-k3"},
		// A board-less selector would match every node, so it has no key.
		{NodeSelector{}, ""},
		{NodeSelector{Provider: ProviderISCAS}, ""},
	}
	for _, tc := range tests {
		if got := tc.sel.Key(); got != tc.want {
			t.Errorf("%+v Key()=%q want %q", tc.sel, got, tc.want)
		}
	}
}

func TestNodeSelectorValid(t *testing.T) {
	if (NodeSelector{Board: BoardSpacemitK3}).Valid() != true {
		t.Error("board-only selector should be valid")
	}
	if (NodeSelector{}).Valid() {
		t.Error("zero selector must be invalid")
	}
	// Provider alone cannot constrain placement to a board.
	if (NodeSelector{Provider: ProviderISCAS}).Valid() {
		t.Error("provider-only selector must be invalid")
	}
}

func TestNodeSelectorLabels(t *testing.T) {
	full := NodeSelector{Board: BoardSpacemitK1, Provider: ProviderMengZhuo}
	if got := full.Labels(); !maps.Equal(got, map[string]string{
		LabelBoard: BoardSpacemitK1, LabelProvider: ProviderMengZhuo,
	}) {
		t.Errorf("Labels()=%v", got)
	}
	// An absent provider must be omitted, not written as an empty value: an
	// empty label value is a real constraint in Kubernetes and matches nothing.
	if got := (NodeSelector{Board: BoardSpacemitK1}).Labels(); !maps.Equal(got, map[string]string{
		LabelBoard: BoardSpacemitK1,
	}) {
		t.Errorf("Labels()=%v", got)
	}
	if got := (NodeSelector{}).Labels(); got != nil {
		t.Errorf("Labels()=%v want nil", got)
	}
}

func TestSelectorForBoard(t *testing.T) {
	tests := []struct {
		board string
		want  NodeSelector
	}{
		{BoardScalewayEMRV1, NodeSelector{Board: BoardScalewayEMRV1, Provider: ProviderScaleway}},
		{BoardSpacemitK1, NodeSelector{Board: BoardSpacemitK1, Provider: ProviderCloudV10x}},
		{BoardSpacemitK3, NodeSelector{Board: BoardSpacemitK3, Provider: ProviderISCAS}},
		{BoardSpacemitV100, NodeSelector{Board: BoardSpacemitV100, Provider: ProviderISCAS}},
		// An unrecognised board stays board-only rather than becoming unconstrained.
		{"future-soc", NodeSelector{Board: "future-soc"}},
		{"", NodeSelector{}},
	}
	for _, tc := range tests {
		if got := SelectorForBoard(tc.board); got != tc.want {
			t.Errorf("SelectorForBoard(%q)=%+v want %+v", tc.board, got, tc.want)
		}
	}
}

// A job or worker predating k8s_selector must never yield an unconstrained
// selector, which would match every node in the cluster.
func TestSelectorFallback_NeverEmptyForKnownBoard(t *testing.T) {
	j := Job{K8sPool: BoardSpacemitK3}
	want := NodeSelector{Board: BoardSpacemitK3, Provider: ProviderISCAS}
	if got := j.Selector(); got != want {
		t.Errorf("Job.Selector()=%+v want %+v", got, want)
	}
	w := Worker{K8sPool: BoardSpacemitK1}
	wantW := NodeSelector{Board: BoardSpacemitK1, Provider: ProviderCloudV10x}
	if got := w.Selector(); got != wantW {
		t.Errorf("Worker.Selector()=%+v want %+v", got, wantW)
	}
}

// A stored selector wins over the k8s_pool-derived default: mengzhuo runs on
// spacemit-k1 whose historical provider is cloudv10x.
func TestSelectorFallback_StoredSelectorWins(t *testing.T) {
	stored := NodeSelector{Board: BoardSpacemitK1, Provider: ProviderMengZhuo}
	j := Job{K8sPool: BoardSpacemitK1, K8sSelector: stored}
	if got := j.Selector(); got != stored {
		t.Errorf("Job.Selector()=%+v want %+v", got, stored)
	}
}

func TestNodeSelector_JSONRoundTrip(t *testing.T) {
	for _, sel := range []NodeSelector{
		{Board: BoardSpacemitK3, Provider: ProviderISCAS},
		{Board: BoardSpacemitK3},
	} {
		v, err := sel.Value()
		if err != nil {
			t.Fatalf("Value: %v", err)
		}
		var back NodeSelector
		if err := back.Scan(v); err != nil {
			t.Fatalf("Scan: %v", err)
		}
		if back != sel {
			t.Errorf("round trip got %+v want %+v", back, sel)
		}
	}
}

// The column stores Kubernetes label keys, not the struct's field names, so
// rows written by the backfill keep scanning correctly.
func TestNodeSelector_ValueUsesLabelKeys(t *testing.T) {
	v, err := NodeSelector{Board: BoardSpacemitK3, Provider: ProviderISCAS}.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	got, ok := v.(string)
	if !ok {
		t.Fatalf("Value() returned %T, want string", v)
	}
	for _, want := range []string{`"riseproject.dev/board":"spacemit-k3"`, `"riseproject.dev/provider":"iscas"`} {
		if !strings.Contains(got, want) {
			t.Errorf("Value()=%s missing %s", got, want)
		}
	}
}

// '{}' is what the column holds for rows written before the backfill.
func TestNodeSelector_ScanEmptyObject(t *testing.T) {
	var sel NodeSelector
	if err := sel.Scan([]byte(`{}`)); err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if sel.Valid() {
		t.Errorf("got %+v, want invalid", sel)
	}
	j := Job{K8sPool: BoardSpacemitK3, K8sSelector: sel}
	if got := j.Selector().Key(); got == "" {
		t.Error("empty stored selector must fall back, not stay unconstrained")
	}
}

func TestNodeSelector_ScanNull(t *testing.T) {
	sel := NodeSelector{Board: "x"}
	if err := sel.Scan(nil); err != nil {
		t.Fatalf("Scan(nil): %v", err)
	}
	if sel.Valid() {
		t.Errorf("got %+v, want invalid", sel)
	}
}

func TestNodeSelector_LogValue(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{}))
	sel := NodeSelector{Board: BoardSpacemitK1, Provider: ProviderMengZhuo}
	log.Info("provisioned", "k8s_selector", sel)

	got := buf.String()
	for _, want := range []string{
		"k8s_selector.board=spacemit-k1",
		"k8s_selector.provider=mengzhuo",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("log %q missing %q", got, want)
		}
	}
}

// A selector resolved through the fallback logs the derived provider, so a
// legacy row is not silently indistinguishable from a modern one.
func TestNodeSelector_LogValueFromFallback(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{}))
	log.Info("legacy", "k8s_selector", Job{K8sPool: BoardSpacemitK3}.Selector())

	if got := buf.String(); !strings.Contains(got, "k8s_selector.provider=iscas") {
		t.Errorf("log %q missing derived provider", got)
	}
}
