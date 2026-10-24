package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
)

// A project contains public launch policy, never credential values. Empty
// selections use the same isolated environment without contacting GitHub.
type project struct {
	Version     int               `json:"version"`
	Repository  string            `json:"repository"`
	DefaultMode string            `json:"default_mode"`
	Secrets     string            `json:"secrets"`
	Variables   string            `json:"variables"`
	Prefixes    map[string]string `json:"prefixes"`
	Aliases     map[string]string `json:"aliases"`
	Environment map[string]string `json:"environment"`
}

func readProject(path string) (project, error) {
	var p project
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBundle+1))
	if err != nil || len(data) > maxBundle {
		return p, errors.New("invalid or oversized project configuration")
	}
	if err := uniqueJSON(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return p, errors.New("invalid project configuration JSON")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return p, errors.New("invalid project configuration JSON")
	}
	for name := range fields {
		switch name {
		case "version", "repository", "default_mode", "secrets", "variables", "prefixes", "aliases", "environment":
		default:
			return p, fmt.Errorf("unknown project field %q", name)
		}
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return project{}, errors.New("invalid project configuration")
	}
	_, repo, _ := strings.Cut(p.Repository, "/")
	if p.Version != 1 || !repoName.MatchString(p.Repository) || repo == "." || repo == ".." {
		return project{}, errors.New("project requires version 1 and an OWNER/REPO repository")
	}
	if p.DefaultMode != "preview" && p.DefaultMode != "local" {
		return project{}, errors.New("project default_mode must be preview or local")
	}
	var names []string
	if p.Secrets != "" || p.Variables != "" {
		names, err = requestedNames(p.Secrets, p.Variables)
		if err != nil {
			return project{}, err
		}
	}
	for name, prefix := range p.Prefixes {
		if !slices.Contains(names, name) || prefix == "" || strings.ContainsRune(prefix, 0) {
			return project{}, errors.New("prefix checks must name selected entries and a nonempty prefix")
		}
	}
	for destination, source := range p.Aliases {
		if !validBundleEntry(destination, "") || !slices.Contains(names, source) || slices.Contains(names, destination) {
			return project{}, errors.New("aliases must map new environment names to selected entries")
		}
	}
	for name, value := range p.Environment {
		_, alias := p.Aliases[name]
		if !validBundleEntry(name, value) || alias || slices.Contains(names, name) {
			return project{}, errors.New("fixed environment entries must be valid and distinct from selected entries and aliases")
		}
	}
	return p, nil
}

func projectEnvironment(ctx context.Context, p project, mode string, base []string, fetch func(context.Context, options) (map[string]string, error)) ([]string, error) {
	if mode == "auto" {
		mode = p.DefaultMode
	}
	if mode != "local" && mode != "preview" {
		return nil, errors.New("development mode must be preview or local")
	}
	values := make(map[string]string)
	if mode == "preview" && (p.Secrets != "" || p.Variables != "") {
		selected, err := fetch(ctx, options{Repo: p.Repository, Environment: "Preview", Ref: "main", Secrets: p.Secrets, Variables: p.Variables, CleanEnvironment: true})
		if err != nil {
			return nil, err
		}
		names, err := requestedNames(p.Secrets, p.Variables)
		if err != nil {
			return nil, err
		}
		if len(selected) != len(names) {
			return nil, errors.New("development handoff returned an unexpected selection")
		}
		for _, name := range names {
			value, ok := selected[name]
			if !ok || !validBundleEntry(name, value) {
				return nil, errors.New("development handoff returned an invalid selection")
			}
			if prefix, check := p.Prefixes[name]; check && !strings.HasPrefix(value, prefix) {
				return nil, fmt.Errorf("Preview entry %s does not match its required development prefix", name)
			}
			values[name] = value
		}
		for destination, source := range p.Aliases {
			values[destination] = values[source]
		}
	}
	for name, value := range p.Environment {
		values[name] = value
	}
	return developmentEnvironment(base, values), nil
}

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
	p, err := readProject(*config)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	environment, err := projectEnvironment(ctx, p, *mode, os.Environ(), func(ctx context.Context, o options) (map[string]string, error) {
		return fetchEnvironment(ctx, ghAPI, o)
	})
	cancel()
	stop()
	if err != nil {
		return err
	}
	return launchEnvironment(args[separator+1:], environment, shutdownGrace)
}
