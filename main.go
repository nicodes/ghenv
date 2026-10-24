package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
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

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var repoName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9_.-]+$`)

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
	_, repo, _ := strings.Cut(o.Repo, "/")
	if !repoName.MatchString(o.Repo) || repo == "." || repo == ".." {
		return o, errors.New("--repo must be OWNER/REPO")
	}
	if o.Environment != "Preview" {
		return o, errors.New("local runs only support the Preview environment; production is refused")
	}
	if o.Ref == "" || o.Timeout <= 0 {
		return o, errors.New("ref and a positive timeout are required")
	}
	if _, err := requestedNames(o.Secrets, o.Variables); err != nil {
		return o, err
	}
	o.Command = args[separator+1:]
	return o, nil
}

func requestedNames(secrets, variables string) ([]string, error) {
	var result []string
	for _, group := range []string{secrets, variables} {
		if group == "" {
			continue
		}
		for _, name := range strings.Split(group, ",") {
			if !envName.MatchString(name) || strings.EqualFold(name, "GITHUB_TOKEN") {
				return nil, errors.New("names must be environment variable identifiers; GITHUB_TOKEN cannot be exported")
			}
			if slices.Contains(result, name) {
				return nil, fmt.Errorf("duplicate name %s", name)
			}
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil, errors.New("request at least one name with --secrets or --vars")
	}
	return result, nil
}

func selectValues(secrets, variables map[string]string, secretNames, variableNames string) (map[string]string, error) {
	names, err := requestedNames(secretNames, variableNames)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(names))
	for i, group := range []string{secretNames, variableNames} {
		if group == "" {
			continue
		}
		source := secrets
		if i == 1 {
			source = variables
		}
		for _, name := range strings.Split(group, ",") {
			value, ok := source[name]
			if !ok {
				return nil, fmt.Errorf("requested name %s is unavailable in this workflow environment", name)
			}
			if strings.ContainsRune(value, 0) {
				return nil, fmt.Errorf("%s contains a NUL byte and cannot be an environment value", name)
			}
			result[name] = value
		}
	}
	return result, nil
}

func sealFromEnvironment() error {
	bundle, err := parsePreviewBundle([]byte(os.Getenv("GHENV_PREVIEW_BUNDLE")), os.Getenv("GITHUB_REPOSITORY"))
	if err != nil {
		return err
	}
	values, err := selectValues(bundle.Secrets, bundle.Variables, os.Getenv("GHENV_SECRET_NAMES"), os.Getenv("GHENV_VAR_NAMES"))
	if err != nil {
		return err
	}
	requestID := os.Getenv("GHENV_REQUEST_ID")
	if !requestName.MatchString(requestID) {
		return errors.New("invalid request ID")
	}
	data, err := seal(os.Getenv("GHENV_PUBLIC_KEY"), values, requestID)
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
			values, err = fetchEnvironment(ctx, ghAPI, o)
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
