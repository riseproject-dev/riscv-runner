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
	return id, nil
}
