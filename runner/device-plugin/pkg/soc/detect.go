// SPDX-License-Identifier: MIT

package soc

import (
	"fmt"
	"os"
	"strings"

	"k8s.io/klog/v2"
)

// SoCID is the riscv_hwprobe hardware identity triple.
type SoCID struct {
	MVendorID uint64
	MArchID   uint64
	MImpID    uint64
}

type SoC struct {
	ID   SoCID
	Name string
}

// socs is the hand-maintained list of known SoCs, keyed by the riscv_hwprobe
// identity triple. Read the "riscv_hwprobe IDs: ..." log line on a node to add
// an entry.
var socs = []SoC{
	{Name: "spacemit-k1", ID: SoCID{MVendorID: 0x0000000000000710, MArchID: 0x8000000058000001, MImpID: 0x1000000049772200}},
	{Name: "spacemit-k3", ID: SoCID{MVendorID: 0x0000000000000710, MArchID: 0x8000000058000002, MImpID: 0x0000000033d8a600}},
	{Name: "spacemit-v100", ID: SoCID{MVendorID: 0x0000000000000710, MArchID: 0x8000000058000002, MImpID: 0x0000000004c4d900}},
}

// scalewayEMRV1 is identified by device tree, not hwprobe: its kernel lacks the
// syscall. The ID triple is known and filled in by hand for observability.
var scalewayEMRV1 = SoC{
	Name: "scaleway-em-rv1",
	ID:   SoCID{MVendorID: 0x0, MArchID: 0x0, MImpID: 0x0},
}

// Detect identifies the SoC from the riscv_hwprobe (mvendorid, marchid, mimpid)
// triple. The Scaleway EM-RV1 is special-cased: its kernel lacks the syscall, so
// on probe failure Detect falls back to the device tree for that board alone.
// Anything unrecognized errors, so a node fails loudly rather than mislabeling.
func Detect() (SoC, error) {
	id, err := probeHWID()
	if err != nil {
		klog.Warningf("riscv_hwprobe failed (%v), falling back to device tree", err)
		return detectFromDeviceTree(err)
	}
	klog.Infof("riscv_hwprobe IDs: mvendorid=%#x marchid=%#x mimpid=%#x",
		id.MVendorID, id.MArchID, id.MImpID)
	s, ok := match(id)
	if !ok {
		return SoC{}, fmt.Errorf("no known SoC for riscv_hwprobe IDs "+
			"mvendorid=%#x marchid=%#x mimpid=%#x", id.MVendorID, id.MArchID, id.MImpID)
	}
	return s, nil
}

func match(id SoCID) (SoC, bool) {
	for _, s := range socs {
		if s.ID == id {
			return s, true
		}
	}
	return SoC{}, false
}

// detectFromDeviceTree recognizes only the Scaleway EM-RV1. Any other board is
// reported as the original probe failure.
func detectFromDeviceTree(probeErr error) (SoC, error) {
	compatible := readCompatible()
	if matchScaleway(compatible) {
		return scalewayEMRV1, nil
	}
	return SoC{}, fmt.Errorf("riscv_hwprobe failed and device tree is not a known board: %w", probeErr)
}

// matchScaleway reports whether a device tree "compatible" property (a set of
// null-separated strings) identifies a Scaleway EM-RV1.
func matchScaleway(compatible string) bool {
	const scalewayCompatible = "scaleway,em-rv1"
	for _, entry := range strings.Split(compatible, "\x00") {
		if strings.HasPrefix(strings.TrimSpace(entry), scalewayCompatible) {
			return true
		}
	}
	return false
}

func readCompatible() string {
	paths := []string{
		"/sys/firmware/devicetree/base/compatible",
		"/proc/device-tree/compatible",
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			klog.Infof("Read device tree compatible from %s: %q", p, string(data))
			return string(data)
		}
	}
	klog.Warning("Failed to read device tree compatible from known paths")
	return ""
}
