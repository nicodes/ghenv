//go:build !linux && !darwin

package main

import "errors"

func launch([]string, map[string]string) error {
	return errors.New("ghenv currently supports process supervision on Linux and macOS only")
}
