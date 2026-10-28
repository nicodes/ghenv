//go:build linux || darwin

package provider

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubErrorsAreActionableAndRedacted(t *testing.T) {
	for _, test := range []struct{ diagnostic, code string }{
		{"gh: denied (HTTP 401) synthetic-secret", "github_auth_required"},
		{"gh: denied (HTTP 403) synthetic-secret", "github_access_denied"},
		{"gh: missing (HTTP 404) synthetic-secret", "github_not_found"},
		{"gh: inputs (HTTP 422) synthetic-secret", "github_invalid_dispatch"},
		{"unexpected synthetic-secret", "github_request_failed"},
	} {
		t.Run(test.code, func(t *testing.T) {
			dir := t.TempDir()
			body := "#!/bin/sh\nprintf '%s' '" + test.diagnostic + "' >&2\nexit 1\n"
			if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir)
			_, err := ghAPI(context.Background(), "GET", "repos/example/project/actions/workflows/ghenv.yml", nil)
			if err == nil || !strings.Contains(err.Error(), test.code) || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("unsafe or unhelpful error: %v", err)
			}
		})
	}
}
