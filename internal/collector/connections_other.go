//go:build !linux

package collector

import (
	"context"

	gnet "github.com/shirou/gopsutil/v4/net"
)

func connectionCounts(ctx context.Context) (int, int, error) {
	connections, err := gnet.ConnectionsWithContext(ctx, "all")
	if err != nil {
		return 0, 0, err
	}
	tcp, udp := 0, 0
	for _, connection := range connections {
		switch connection.Type {
		case 1: // SOCK_STREAM
			tcp++
		case 2: // SOCK_DGRAM
			udp++
		}
	}
	return tcp, udp, nil
}
