package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/nicodes/ghenv/provider"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
)

const usage = `ghenv: run a local command with GitHub Actions secrets and variables.

Usage:
  ghenv dev [--mode auto|preview|local] [--config ghenv.json] -- COMMAND [ARG...]
  ghenv run --repo OWNER/REPO --secrets NAME,OTHER --vars NAME -- COMMAND [ARG...]
  ghenv local -- COMMAND [ARG...]  (clean, credential-free; no GitHub request)

Options (before --):
  --repo       GitHub repository containing the ghenv.yml workflow (required)
  --env        GitHub environment (only Preview is permitted)
  --ref        Trusted workflow Git ref (default: main)
  --secrets    Comma-separated secret names to inject
  --vars       Comma-separated variable names to inject
  --timeout    Maximum time to wait for Actions (default: 5m)
  --clean-env  Inherit only terminal/tool paths, then inject requested values

Requires gh auth login and Actions write access. No .env files are written.
`

type options struct {
	Repo, Environment, Ref, Secrets, Variables string
	Timeout                                    time.Duration
	Command                                    []string
	CleanEnvironment                           bool
}

func parseOptions(args []string) (options, error) {
	var o options
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&o.Repo, "repo", "", "GitHub OWNER/REPO")
	fs.StringVar(&o.Environment, "env", "Preview", "GitHub environment (Preview only)")
	fs.StringVar(&o.Ref, "ref", "main", "trusted workflow Git ref")
	fs.StringVar(&o.Secrets, "secrets", "", "comma-separated secret names")
	fs.StringVar(&o.Variables, "vars", "", "comma-separated variable names")
	fs.DurationVar(&o.Timeout, "timeout", 5*time.Minute, "workflow timeout")
	fs.BoolVar(&o.CleanEnvironment, "clean-env", false, "isolate child credentials from the parent environment")
	separator := slices.Index(args, "--")
	if separator < 0 || separator == len(args)-1 {
		return o, errors.New("specify a command after --")
	}
	if err := fs.Parse(args[:separator]); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, errors.New("unexpected argument before --")
	}
	if o.Timeout <= 0 {
		return o, errors.New("a positive timeout is required")
	}
	if err := (provider.Request{Repo: o.Repo, Environment: o.Environment, Ref: o.Ref, Secrets: o.Secrets, Variables: o.Variables}).Validate(); err != nil {
		return o, err
	}

	o.Command = args[separator+1:]
	return o, nil
}

func sealFromEnvironment() error {
	data, err := provider.SealBundle([]byte(os.Getenv("GHENV_PREVIEW_BUNDLE")), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GHENV_SECRET_NAMES"), os.Getenv("GHENV_VAR_NAMES"), os.Getenv("GHENV_PUBLIC_KEY"), os.Getenv("GHENV_REQUEST_ID"))
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

func commandEnvironment(base []string, values map[string]string) []string {
	result := make([]string, 0, len(base)+len(values))
	for _, item := range base {
		name, _, _ := strings.Cut(item, "=")
		if _, replace := values[name]; !replace {
			result = append(result, item)
		}
	}
	for name, value := range values {
		result = append(result, name+"="+value)
	}
	return result
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "help" || os.Args[1] == "--help" || os.Args[1] == "-h" {
		fmt.Print(usage)
		return
	}
	var err error
	switch os.Args[1] {
	case "dev":
		err = runDevelopment(os.Args[2:])
	case "local":
		if len(os.Args) < 4 || os.Args[2] != "--" {
			err = errors.New("usage: ghenv local -- COMMAND [ARG...]")
		} else {
			err = launchEnvironment(os.Args[3:], developmentEnvironment(os.Environ(), nil), shutdownGrace)
		}
	case "seal":
		err = sealFromEnvironment()
	case "run":
		var o options
		o, err = parseOptions(os.Args[2:])
		if err == nil {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			ctx, cancel := context.WithTimeout(ctx, o.Timeout)
			var values map[string]string
			values, err = provider.Fetch(ctx, provider.Request{Repo: o.Repo, Environment: o.Environment, Ref: o.Ref, Secrets: o.Secrets, Variables: o.Variables})
			cancel()
			stop()
			if err == nil {
				if o.CleanEnvironment {
					err = launchEnvironment(o.Command, developmentEnvironment(os.Environ(), values), shutdownGrace)
				} else {
					err = launch(o.Command, values)
				}
			}
		}
	default:
		err = fmt.Errorf("unknown command %q; use ghenv --help", os.Args[1])
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code := exit.ExitCode()
			if code < 0 {
				code = 1
			}
			os.Exit(code)
		}
		fmt.Fprintln(os.Stderr, "ghenv:", err)
		os.Exit(1)
	}
}
