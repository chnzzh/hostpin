//go:build windows

package service

func isRoot() bool { return true }

func currentUID() int { return 0 }
