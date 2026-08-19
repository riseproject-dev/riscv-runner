// SPDX-License-Identifier: MIT

package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"

	"github.com/riseproject-dev/riscv-runner/runner/device-plugin/pkg/labeler"
	"github.com/riseproject-dev/riscv-runner/runner/device-plugin/pkg/plugin"
	"github.com/riseproject-dev/riscv-runner/runner/device-plugin/pkg/soc"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		klog.Fatal("NODE_NAME environment variable is required")
	}

	detected, err := soc.Detect()
	if err != nil {
		klog.Fatalf("Failed to detect SoC: %v", err)
	}
	klog.Infof("Detected board: %s (mvendorid=%#x marchid=%#x mimpid=%#x)",
		detected.Name, detected.ID.MVendorID, detected.ID.MArchID, detected.ID.MImpID)

	if err := labeler.LabelNode(nodeName, detected.Name); err != nil {
		klog.Fatalf("Failed to label node: %v", err)
	}

	p := plugin.New(detected)
	if err := p.Start(); err != nil {
		klog.Fatalf("Failed to start device plugin: %v", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	klog.Infof("Received signal %s, shutting down", s)

	p.Stop()
}
