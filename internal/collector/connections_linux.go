//go:build linux

package collector

import (
	"bufio"
	"context"
	"os"
)

func connectionCounts(_ context.Context) (int, int, error) {
	tcp := countProcConnections([]string{"/proc/net/tcp", "/proc/net/tcp6"})
	udp := countProcConnections([]string{"/proc/net/udp", "/proc/net/udp6"})
	return tcp, udp, nil
}

func countProcConnections(paths []string) int {
	total := 0
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		first := true
		for scanner.Scan() {
			if first {
				first = false
				continue
			}
			total++
		}
		file.Close()
	}
	return total
}
