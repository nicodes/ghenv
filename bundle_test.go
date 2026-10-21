package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const testBundle = `{"version":1,"environment":"Preview","repository":"owner/repo","secrets":{"TOKEN":"synthetic secret"},"variables":{"URL":"http://localhost"}}`

func TestBundleScope(t *testing.T) {
	got, err := parsePreviewBundle([]byte(testBundle), "owner/repo")
	if err != nil || got.Secrets["TOKEN"] != "synthetic secret" {
		t.Fatalf("valid bundle rejected: %v", err)
	}
	for _, data := range []string{
		"", "null", "{}", testBundle + testBundle,
		strings.Replace(testBundle, `"Preview"`, `"Production"`, 1),
		strings.Replace(testBundle, `"owner/repo"`, `"owner/other"`, 1),
		strings.Replace(testBundle, `"version":1`, `"version":2`, 1),
		strings.Replace(testBundle, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(testBundle, `"version":1`, `"version":1,"Version":1`, 1),
		strings.Replace(testBundle, `"version":1`, `"version":1,"unexpected":true`, 1),
		strings.Replace(testBundle, `"URL":"http://localhost"`, `"TOKEN":"duplicate"`, 1),
		strings.Replace(testBundle, `"TOKEN"`, `"GITHUB_TOKEN"`, 1),
		strings.Replace(testBundle, `"TOKEN"`, `"INVALID-NAME"`, 1),
		strings.Replace(testBundle, `"synthetic secret"`, `"bad\u0000value"`, 1),
		strings.Replace(testBundle, `"synthetic secret"`, `42`, 1),
		strings.Replace(testBundle, `"synthetic secret"`, `null`, 1),
		strings.Replace(testBundle, `"http://localhost"`, `null`, 1),
		strings.Replace(testBundle, `"secrets":{"TOKEN":"synthetic secret"}`, `"secrets":null`, 1),
		strings.Replace(testBundle, `"variables":{"URL":"http://localhost"}`, `"variables":null`, 1),
		strings.Replace(testBundle, `"secrets"`, `"ſecrets"`, 1),
		strings.Replace(testBundle, `"repository"`, `"repoſitory"`, 1),
		strings.Replace(testBundle, `"version":1`, `"Version":1`, 1),
		strings.Replace(testBundle, `"secrets":`, `"ſecrets":{"EXTRA":"sentinel"},"secrets":`, 1),
		strings.Replace(testBundle, `,"variables":{"URL":"http://localhost"}`, ``, 1),
		strings.Repeat(" ", maxBundle+1),
	} {
		if _, err := parsePreviewBundle([]byte(data), "owner/repo"); err == nil {
			t.Fatal("invalid or ambiguous bundle accepted")
		}
	}
	if _, err := parsePreviewBundle([]byte(testBundle), ""); err == nil {
		t.Fatal("accepted missing trusted workflow repository")
	}
}

func TestDirectSealCannotExportSurroundingSecrets(t *testing.T) {
	_, public := keyPair(t)
	for _, tc := range []struct{ name, bundle, requested string }{
		{"legacy-merged-context", "", "PROD_ONLY"},
		{"outside-bundle", testBundle, "PROD_ONLY"},
		{"production-bundle-fallback", strings.Replace(testBundle, `"Preview"`, `"Production"`, 1), "TOKEN"},
		{"foreign-repository", strings.Replace(testBundle, `"owner/repo"`, `"owner/other"`, 1), "TOKEN"},
		{"null-value", strings.Replace(testBundle, `"synthetic secret"`, `null`, 1), "TOKEN"},
		{"unicode-alias", strings.Replace(testBundle, `"secrets":`, `"ſecrets":{"PROD_ONLY":"sentinel"},"secrets":`, 1), "PROD_ONLY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSealHelper$")
			cmd.Env = commandEnvironment(os.Environ(), map[string]string{
				"GHENV_SEAL_TEST_CHILD": "yes", "GITHUB_REPOSITORY": "owner/repo",
				"GHENV_PREVIEW_BUNDLE": tc.bundle, "GHENV_SECRET_NAMES": tc.requested,
				"GHENV_VAR_NAMES": "", "GHENV_PUBLIC_KEY": public,
				"GHENV_REQUEST_ID":   "0123456789abcdef0123456789abcdef",
				"GHENV_SECRETS_JSON": `{"PROD_ONLY":"synthetic-production-sentinel"}`,
				"GHENV_VARS_JSON":    `{"PROD_ONLY":"synthetic-production-sentinel"}`,
			})
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			output, err := cmd.Output()
			if err == nil || len(output) != 0 {
				t.Fatal("direct exporter bypass produced output")
			}
			if bytes.Contains(stderr.Bytes(), []byte("synthetic-production-sentinel")) {
				t.Fatal("failure leaked secret")
			}
		})
	}
}

func TestWorkflowExposesOnlyBundle(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/ghenv.yml")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("toJSON(secrets)")) || bytes.Contains(data, []byte("toJSON(vars)")) {
		t.Fatal("workflow exposes merged GitHub context")
	}
	if bytes.Count(data, []byte("${{ secrets.")) != 1 || !bytes.Contains(data, []byte("${{ secrets.GHENV_PREVIEW_BUNDLE }}")) {
		t.Fatal("workflow must expose only the fixed Preview bundle")
	}
}
