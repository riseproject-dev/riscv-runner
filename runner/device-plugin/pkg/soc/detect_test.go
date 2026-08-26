// SPDX-License-Identifier: MIT

package soc

import "testing"

func TestMatch(t *testing.T) {
	socs = []SoC{
		{Name: "test-soc", ID: SoCID{MVendorID: 0x710, MArchID: 0x8000000058000001, MImpID: 0x1}},
	}

	tests := []struct {
		name     string
		id       SoCID
		wantName string
		wantOK   bool
	}{
		{
			name:     "hit",
			id:       SoCID{MVendorID: 0x710, MArchID: 0x8000000058000001, MImpID: 0x1},
			wantName: "test-soc",
			wantOK:   true,
		},
		{
			name:   "miss on mimpid",
			id:     SoCID{MVendorID: 0x710, MArchID: 0x8000000058000001, MImpID: 0x2},
			wantOK: false,
		},
		{
			name:   "miss on all zero",
			id:     SoCID{},
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := match(tt.id)
			if ok != tt.wantOK {
				t.Fatalf("match(%+v) ok = %v, want %v", tt.id, ok, tt.wantOK)
			}
			if ok && got.Name != tt.wantName {
				t.Errorf("match(%+v) name = %q, want %q", tt.id, got.Name, tt.wantName)
			}
		})
	}
}

func TestMatchScaleway(t *testing.T) {
	tests := []struct {
		name       string
		compatible string
		want       bool
	}{
		{"exact prefix", "scaleway,em-rv1-c4m16s128-a", true},
		{"trailing entries", "scaleway,em-rv1\x00riscv", true},
		{"leading whitespace", "  scaleway,em-rv1  ", true},
		{"other board", "spacemit,k1-x", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchScaleway(tt.compatible); got != tt.want {
				t.Errorf("matchScaleway(%q) = %v, want %v", tt.compatible, got, tt.want)
			}
		})
	}
}
