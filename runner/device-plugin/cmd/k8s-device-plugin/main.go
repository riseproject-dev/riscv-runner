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

	board := soc.DetectBoard()
	klog.Infof("Detected board: %s", board)

	if err := labeler.LabelNode(nodeName, board); err != nil {
		klog.Fatalf("Failed to label node: %v", err)
	}

	p := plugin.New()
	if err := p.Start(); err != nil {
		klog.Fatalf("Failed to start device plugin: %v", err)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	s := <-sig
	klog.Infof("Received signal %s, shutting down", s)

	p.Stop()
}
