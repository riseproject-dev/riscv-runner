package soc

import (
	"os"
	"strings"

	"k8s.io/klog/v2"
)

// boardMap maps device tree compatible strings to board names.
var boardMap = map[string]string{
	"scaleway,em-rv1-c4m16s128-a": "scw-em-rv1",
	"sophgo,mango":                "cloudv10x-pioneer",
	"spacemit,k1-x":               "cloudv10x-jupiter",
}

// DetectBoard reads the device tree compatible property and maps it to a board name.
func DetectBoard() string {
	compatible := readCompatible()
	if compatible == "" {
		klog.Warning("Could not read device tree compatible string")
		return "<unknown>"
	}

	entries := strings.Split(compatible, "\x00")
	klog.Infof("Device tree compatible entries: %v", entries)
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		klog.Infof("Checking compatible entry: '%s'", entry)
		if entry == "" {
			continue
		}
		if board, ok := boardMap[entry]; ok {
			klog.Infof("Matched compatible '%s' to board '%s'", entry, board)
			return board
		}
	}

	// Fall back to first compatible entry, sanitized
	if len(entries) > 0 && strings.TrimSpace(entries[0]) != "" {
		fallback := sanitize(strings.TrimSpace(entries[0]))
		klog.Infof("No known mapping for compatible string, using: %s", fallback)
		return fallback
	}

	return "<unknown>"
}

func readCompatible() string {
	paths := []string{
		"/sys/firmware/devicetree/base/compatible",
		"/proc/device-tree/compatible",
	}
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			klog.Infof("Read compatible string from %s: %s", p, string(data))
			return string(data)
		}
	}
	klog.Warning("Failed to read compatible string from known paths")
	return ""
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, ",", "-")
	s = strings.ReplaceAll(s, " ", "-")
	s = strings.ToLower(s)
	return s
}
