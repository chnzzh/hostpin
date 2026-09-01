//go:build !windows

package main

import "context"

func executeAsService(func(context.Context) error) (bool, error) { return false, nil }
