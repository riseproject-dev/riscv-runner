package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"

	"github.com/riseproject/riscv-runner-device-plugin/pkg/labeler"
	"github.com/riseproject/riscv-runner-device-plugin/pkg/soc"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

	board := soc.DetectBoard()
	klog.Infof("Detected board: %s", board)

	nodeName := os.Getenv("NODE_NAME")
	if nodeName == "" {
		klog.Fatal("NODE_NAME environment variable is required")
	}

	if err := labeler.LabelNode(nodeName, board); err != nil {
		klog.Fatalf("Failed to label node: %v", err)
	}

	klog.Infof("Node %s labeled with board=%s, waiting for termination signal", nodeName, board)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
}
