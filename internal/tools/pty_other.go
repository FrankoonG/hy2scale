//go:build !linux

package tools

import (
	"errors"
	"runtime"
)

func terminalAvailable() (bool, string) {
	return false, "not supported on " + runtime.GOOS
}

func startShell(cols, rows int) (shellSession, error) {
	return nil, errors.New("terminal not supported on " + runtime.GOOS)
}
