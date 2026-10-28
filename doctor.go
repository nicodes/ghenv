package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/nicodes/ghenv/provider"
)

func runDoctor(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	repo := fs.String("repo", "", "GitHub OWNER/REPO")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: ghenv doctor --repo OWNER/REPO")
	}
	if err := (provider.Request{Repo: *repo, Environment: "Preview", Ref: "main", Variables: "GHENV_PREFLIGHT"}).Validate(); err != nil {
		return err
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return errors.New("github_cli_missing: install GitHub CLI (gh), then run gh auth login")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "auth", "status", "--hostname", "github.com")
	if err := cmd.Run(); err != nil {
		return errors.New("github_auth_required: run gh auth login --hostname github.com")
	}
	if err := provider.CheckWorkflow(ctx, *repo); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "GitHub authentication: ready\nghenv.yml workflow: active\nDispatch permissions, Preview approval and requested bundle names are checked when you run a command.\nUse ghenv local -- COMMAND for an offline session.")
	return nil
}
