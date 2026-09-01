//go:build linux

package collector

import (
	"context"
	"os"
	"strings"
)

func bootID(_ context.Context) string {
	data, _ := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(data))
}
