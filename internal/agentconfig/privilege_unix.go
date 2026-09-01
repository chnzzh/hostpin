//go:build !windows

package agentconfig

import "os"

func isPrivileged() bool { return os.Geteuid() == 0 }
