//go:build !windows

package service

import "os"

func isRoot() bool { return os.Geteuid() == 0 }

func currentUID() int { return os.Getuid() }
