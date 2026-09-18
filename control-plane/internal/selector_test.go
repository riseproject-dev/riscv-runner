// SPDX-License-Identifier: MIT

package internal

import (
	"bytes"
	"encoding/json"
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

// The DB column keeps the Kubernetes label form. JSON uses the struct's field
// names, which is what /jobs.json and /workers.json now expose.
func TestNodeSelector_ValueUsesLabelKeysJSONUsesFields(t *testing.T) {
	sel := NodeSelector{Board: BoardSpacemitK3, Provider: ProviderISCAS}

	v, err := sel.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	wantDB := `{"riseproject.dev/board":"spacemit-k3","riseproject.dev/provider":"iscas"}`
	if got := v.(string); got != wantDB {
		t.Errorf("Value()=%s want %s", got, wantDB)
	}

	b, err := json.Marshal(sel)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if want := `{"board":"spacemit-k3","provider":"iscas"}`; string(b) != want {
		t.Errorf("Marshal()=%s want %s", b, want)
	}
}

// A board-only selector omits provider in both forms.
func TestNodeSelector_BoardOnlyOmitsProvider(t *testing.T) {
	sel := NodeSelector{Board: BoardSpacemitK3}
	v, err := sel.Value()
	if err != nil {
		t.Fatalf("Value: %v", err)
	}
	if got := v.(string); got != `{"riseproject.dev/board":"spacemit-k3"}` {
		t.Errorf("Value()=%s", got)
	}
	b, _ := json.Marshal(sel)
	if want := `{"board":"spacemit-k3"}`; string(b) != want {
		t.Errorf("Marshal()=%s want %s", b, want)
	}
}
