//go:build linux || darwin

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionAndDoctorCLI(t *testing.T) {
	for _, scenario := range []string{"version", "ready", "auth", "workflow", "missing-gh"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			if scenario != "missing-gh" {
				script := "#!/bin/sh\nif [ \"$1\" = auth ]; then "
				if scenario == "auth" {
					script += "exit 1"
				} else {
					script += "exit 0"
				}
				script += "; fi\n"
				if scenario == "workflow" {
					script += "printf 'gh: Not Found (HTTP 404) synthetic-secret' >&2; exit 1\n"
				} else {
					script += "printf '{\"state\":\"active\"}'\n"
				}
				if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			args := []string{"-test.run=^TestCLIHelper$", "--", "doctor", "--repo", "example/project"}
			if scenario == "version" {
				args = []string{"-test.run=^TestCLIHelper$", "--", "--version"}
			}
			cmd := exec.Command(os.Args[0], args...)
			cmd.Env = commandEnvironment(os.Environ(), map[string]string{"PATH": dir, "GHENV_CLI_TEST": "yes"})
			out, err := cmd.CombinedOutput()
			text := string(out)
			if scenario == "version" || scenario == "ready" {
				if err != nil {
					t.Fatalf("%v %s", err, text)
				}
			} else if err == nil {
				t.Fatal("invalid setup accepted")
			}
			if strings.Contains(text, "synthetic-secret") {
				t.Fatal("raw diagnostics leaked")
			}
			wants := map[string]string{"version": "ghenv ", "ready": "workflow: active", "auth": "github_auth_required", "workflow": "github_not_found", "missing-gh": "github_cli_missing"}
			if !strings.Contains(text, wants[scenario]) {
				t.Fatalf("missing diagnostic %q: %s", wants[scenario], text)
			}
		})
	}
}
