package main

import (
	"context"
	"errors"
	"flag"
	"github.com/nicodes/ghenv/provider"
	"os"
	"os/signal"
	"slices"
	"syscall"
	"time"
)

func runDevelopment(args []string) error {
	fs := flag.NewFlagSet("dev", flag.ContinueOnError)
	config := fs.String("config", "ghenv.json", "public project configuration")
	mode := fs.String("mode", "auto", "auto, preview or local")
	timeout := fs.Duration("timeout", 5*time.Minute, "maximum time to wait for Actions")
	separator := slices.Index(args, "--")
	if separator < 0 || separator == len(args)-1 {
		return errors.New("usage: ghenv dev [--mode auto|preview|local] [--config ghenv.json] -- COMMAND [ARG...]")
	}
	if err := fs.Parse(args[:separator]); err != nil {
		return err
	}
	if fs.NArg() != 0 || *timeout <= 0 || (*mode != "auto" && *mode != "preview" && *mode != "local") {
		return errors.New("development mode must be auto, preview or local, with a positive timeout and no positional options")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	environment, err := provider.Prepare(ctx, *config, *mode, os.Environ())
	cancel()
	stop()
	if err != nil {
		return err
	}
	return launchEnvironment(args[separator+1:], environment, shutdownGrace)
}
