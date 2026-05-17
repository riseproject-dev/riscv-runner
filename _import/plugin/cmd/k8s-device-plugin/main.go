package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"

	"github.com/riseproject/riscv-runner-device-plugin/pkg/plugin"
)

func main() {
	klog.InitFlags(nil)
	flag.Parse()

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
