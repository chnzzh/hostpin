//go:build windows

package main

import "os"

func platformSignals() []os.Signal { return []os.Signal{os.Interrupt} }
