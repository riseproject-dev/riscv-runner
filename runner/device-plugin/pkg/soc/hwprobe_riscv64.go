// SPDX-License-Identifier: MIT

//go:build riscv64 && linux

package soc

import "golang.org/x/sys/unix"

func probeHWID() (SoCID, error) {
	pairs := []unix.RISCVHWProbePairs{
		{Key: unix.RISCV_HWPROBE_KEY_MVENDORID},
		{Key: unix.RISCV_HWPROBE_KEY_MARCHID},
		{Key: unix.RISCV_HWPROBE_KEY_MIMPID},
	}
	if err := unix.RISCVHWProbe(pairs, nil, 0); err != nil {
		return SoCID{}, err
	}
	var id SoCID
	for _, p := range pairs {
		switch p.Key {
		case unix.RISCV_HWPROBE_KEY_MVENDORID:
			id.MVendorID = p.Value
		case unix.RISCV_HWPROBE_KEY_MARCHID:
			id.MArchID = p.Value
		case unix.RISCV_HWPROBE_KEY_MIMPID:
			id.MImpID = p.Value
		}
	}
	// Under riscv_hwprobe(2), querying across all CPUs returns -1 (^uint64(0)) for
	// keys that differ between CPUs (heterogeneous multi-cluster SoCs). If that
	// happens, re-probe CPU 0 specifically to obtain the primary cluster IDs.
	if id.MArchID == ^uint64(0) || id.MImpID == ^uint64(0) {
		var set unix.CPUSet
		set[0] = 1 // CPU 0
		pairs[0].Value = 0
		pairs[1].Value = 0
		pairs[2].Value = 0
		if err := unix.RISCVHWProbe(pairs, &set, 0); err == nil {
			for _, p := range pairs {
				switch p.Key {
				case unix.RISCV_HWPROBE_KEY_MVENDORID:
					id.MVendorID = p.Value
				case unix.RISCV_HWPROBE_KEY_MARCHID:
					id.MArchID = p.Value
				case unix.RISCV_HWPROBE_KEY_MIMPID:
					id.MImpID = p.Value
				}
			}
		}
	}
	return id, nil
}
