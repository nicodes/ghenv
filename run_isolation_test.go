package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDefaultIsolatesParentCredentials(t *testing.T) {
	for _, clean := range []bool{true, false} {
		dir := t.TempDir()
		shim := "#!/bin/sh\nexec \"$GHENV_TEST_EXECUTABLE\" -test.run=^TestFakeGH$ -- \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(shim), 0700); err != nil {
			t.Fatal(err)
		}
		args := []string{"-test.run=^TestCLIHelper$", "--", "run", "--repo", "owner/repo", "--secrets", "GHENV_TEST_CHILD,GHENV_TEST_VALUE", "--vars", "GHENV_TEST_EMPTY"}
		if !clean {
			args = append(args, "--clean-env=false")
		}
		args = append(args, "--", "/bin/sh", "-c", `if [ -n "${GH_TOKEN+x}" ]; then echo parent_auth_reached_child=true; else echo parent_auth_reached_child=false; fi; if [ "${EXPO_NO_DOTENV-}" = 1 ]; then echo dotenv_disabled=true; else echo dotenv_disabled=false; fi`)
		cmd := exec.Command(os.Args[0], args...)
		cmd.Env = commandEnvironment(os.Environ(), map[string]string{
			"PATH": dir + string(os.PathListSeparator) + os.Getenv("PATH"), "GHENV_TEST_EXECUTABLE": os.Args[0], "GHENV_TEST_STATE": dir, "GHENV_CLI_TEST": "yes", "GH_TOKEN": "synthetic-parent-only",
		})
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("probe failed: %v %s", err, out)
		}
		if strings.Contains(string(out), "synthetic-parent-only") {
			t.Fatal("probe printed synthetic credential value")
		}
		inherited := strings.Contains(string(out), "parent_auth_reached_child=true")
		dotenv := strings.Contains(string(out), "dotenv_disabled=true")
		t.Logf("clean=%v parent_auth_reached_child=%v dotenv_disabled=%v", clean, inherited, dotenv)
		if inherited == clean || dotenv != clean {
			t.Fatal("unexpected credential or dotenv boundary")
		}
	}
}
