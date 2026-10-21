//go:build !linux && !darwin

package main

import (
	"errors"
	"time"
)

const shutdownGrace = 5 * time.Second

func launchEnvironment([]string, []string, time.Duration) error {
	return errors.New("ghenv currently supports process supervision on Linux and macOS only")
}

func launch([]string, map[string]string) error {
	return errors.New("ghenv currently supports process supervision on Linux and macOS only")
}
