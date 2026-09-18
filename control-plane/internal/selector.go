// SPDX-License-Identifier: MIT

package internal

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log/slog"
)

const (
	LabelBoard    = "riseproject.dev/board"
	LabelProvider = "riseproject.dev/provider"
)

// Board values must match the SoC names the device plugin writes to
// riseproject.dev/board (runner/device-plugin/pkg/soc/detect.go). A rename on
// either side strands jobs at available=0.
const (
	BoardScalewayEMRV1 = "scaleway-em-rv1"
	BoardSpacemitK1    = "spacemit-k1"
	BoardSpacemitK3    = "spacemit-k3"
	BoardSpacemitV100  = "spacemit-v100"
)

// Providers are applied to nodes by hand; nothing detects who owns a machine.
const (
	ProviderScaleway  = "scaleway"
	ProviderCloudV10x = "cloudv10x"
	ProviderISCAS     = "iscas"
	ProviderMengZhuo  = "mengzhuo"
)

// ErrEmptySelector guards the node queries: a selector without a board would
// match every node in the cluster, so callers must fail loudly rather than
// place a runner on arbitrary hardware.
var ErrEmptySelector = fmt.Errorf("node selector has no board")

// NodeSelector is the node labels a runner pod must match. Board is mandatory.
// Provider is optional: empty means any vendor's machine of that board.
type NodeSelector struct {
	Board    string `json:"board"`
	Provider string `json:"provider,omitempty"`
}

// Valid reports whether this selector constrains placement at all. An invalid
// selector must never reach Kubernetes.
func (s NodeSelector) Valid() bool { return s.Board != "" }

// Labels is the selector as Kubernetes node labels, for a pod's nodeSelector
// and for the JSONB column.
func (s NodeSelector) Labels() map[string]string {
	if !s.Valid() {
		return nil
	}
	out := map[string]string{LabelBoard: s.Board}
	if s.Provider != "" {
		out[LabelProvider] = s.Provider
	}
	return out
}

// Key is the canonical label-selector form, used both as the demandMatch
// grouping key and as the Kubernetes label selector. Field order is fixed, so
// equal selectors always produce one capacity lookup.
func (s NodeSelector) Key() string {
	if !s.Valid() {
		return ""
	}
	key := LabelBoard + "=" + s.Board
	if s.Provider != "" {
		key += "," + LabelProvider + "=" + s.Provider
	}
	return key
}

// LogValue makes NodeSelector an slog.LogValuer, so board and provider stay
// queryable instead of being buried in one string.
func (s NodeSelector) LogValue() slog.Value {
	attrs := []slog.Attr{slog.String("board", s.Board)}
	if s.Provider != "" {
		attrs = append(attrs, slog.String("provider", s.Provider))
	}
	return slog.GroupValue(attrs...)
}

// Value implements driver.Valuer. The column holds the label map rather than
// the struct's field names, so it stays readable in SQL and matches what the
// backfill wrote.
func (s NodeSelector) Value() (driver.Value, error) {
	labels := s.Labels()
	if labels == nil {
		return "{}", nil
	}
	b, err := json.Marshal(labels)
	if err != nil {
		return nil, err
	}
	return string(b), nil
}

func (s *NodeSelector) Scan(src any) error {
	var b []byte
	switch v := src.(type) {
	case nil:
		*s = NodeSelector{}
		return nil
	case []byte:
		b = v
	case string:
		b = []byte(v)
	default:
		return fmt.Errorf("cannot scan %T into NodeSelector", src)
	}
	var m map[string]string
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	*s = NodeSelector{Board: m[LabelBoard], Provider: m[LabelProvider]}
	return nil
}
