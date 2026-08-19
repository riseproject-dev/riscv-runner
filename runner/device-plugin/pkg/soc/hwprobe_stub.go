// SPDX-License-Identifier: MIT

//go:build !(riscv64 && linux)

package soc

import "errors"

func probeHWID() (SoCID, error) {
	return SoCID{}, errors.New("riscv_hwprobe is only supported on riscv64 linux")
}
