//go:build !linux

package collector

import (
	"context"
	"strconv"

	"github.com/shirou/gopsutil/v4/host"
)

func bootID(ctx context.Context) string {
	value, _ := host.BootTimeWithContext(ctx)
	return strconv.FormatUint(value, 10)
}
