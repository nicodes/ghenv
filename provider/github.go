package provider

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var requestName = regexp.MustCompile(`^[a-f0-9]{32}$`)

type apiFunc func(context.Context, string, string, any) ([]byte, error)

// Bound captured output, including ZIP archives, before buffering it in memory.
type limitedBuffer struct{ buffer bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.buffer.Len()+len(p) > maxPayload*2 {
		return 0, errors.New("GitHub response exceeds size limit")
	}
	return b.buffer.Write(p)
}

func ghAPI(ctx context.Context, method, path string, body any) ([]byte, error) {
	args := []string{"api", "--hostname", "github.com", "--method", method, path,
		"-H", "Accept: application/vnd.github+json", "-H", "X-GitHub-Api-Version: 2022-11-28"}
	var input []byte
	var err error
	if body != nil {
		input, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
		args = append(args, "--input", "-")
	}
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdin = bytes.NewReader(input)
	var output limitedBuffer
	cmd.Stdout = &output
	// Do not echo API bodies or credentials from subprocess diagnostics.
	var diagnostic limitedBuffer
	cmd.Stderr = &diagnostic
	if err = cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("github_cli_missing: install GitHub CLI (gh), then run gh auth login")
		}
		code, message := "github_request_failed", "run ghenv doctor --repo OWNER/REPO to check setup"
		text := diagnostic.buffer.String()
		switch {
		case strings.Contains(text, "HTTP 401") || strings.Contains(text, "gh auth login"):
			code, message = "github_auth_required", "run gh auth login --hostname github.com"
		case strings.Contains(text, "HTTP 403") || strings.Contains(text, "HTTP 429"):
			code, message = "github_access_denied", "check Actions read/write permission, repository access and GitHub rate limits"
		case strings.Contains(text, "HTTP 404"):
			code, message = "github_not_found", "check repository access and install .github/workflows/ghenv.yml on the default branch"
		case strings.Contains(text, "HTTP 422"):
			code, message = "github_invalid_dispatch", "check the trusted workflow ref and ghenv workflow inputs"
		}
		return nil, fmt.Errorf("%s: GitHub %s %s failed; %s", code, method, strings.Split(path, "?")[0], message)
	}
	return output.buffer.Bytes(), nil
}

func fetchEnvironment(ctx context.Context, api apiFunc, o Request) (map[string]string, error) {
	names, err := requestedNames(o.Secrets, o.Variables)
	if err != nil {
		return nil, err
	}
	if o.Environment != "Preview" {
		return nil, errors.New("local runs only support the Preview environment; production is refused")
	}
	privateKey, err := rsa.GenerateKey(rand.Reader, 3072)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return nil, err
	}
	idBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		return nil, err
	}
	requestID := hex.EncodeToString(idBytes)
	prefix := "repos/" + o.Repo + "/actions"
	body := map[string]any{
		"ref":                o.Ref,
		"return_run_details": true,
		"inputs": map[string]string{
			"environment": o.Environment, "request_id": requestID,
			"public_key":   base64.StdEncoding.EncodeToString(der),
			"secret_names": o.Secrets, "variable_names": o.Variables,
		},
	}
	data, err := api(ctx, "POST", prefix+"/workflows/ghenv.yml/dispatches", body)
	if err != nil {
		return nil, err
	}
	var dispatch struct {
		ID int64 `json:"workflow_run_id"`
	}
	if json.Unmarshal(data, &dispatch) != nil || dispatch.ID == 0 {
		return nil, errors.New("GitHub did not return a workflow run ID; github.com workflow dispatch run details are required")
	}
	runPath := fmt.Sprintf("%s/runs/%d", prefix, dispatch.ID)
	fmt.Fprintf(os.Stderr, "ghenv: waiting for https://github.com/%s/actions/runs/%d\n", o.Repo, dispatch.ID)
	previousStatus := ""
	for {
		data, err = api(ctx, "GET", runPath, nil)
		if err != nil {
			return nil, err
		}
		var run struct{ Status, Conclusion string }
		if err = json.Unmarshal(data, &run); err != nil {
			return nil, errors.New("invalid workflow run response")
		}
		if run.Status == "waiting" && run.Status != previousStatus {
			fmt.Fprintln(os.Stderr, "ghenv: waiting for Preview environment approval; approve the linked workflow run")
		}
		previousStatus = run.Status
		if run.Status == "completed" {
			if run.Conclusion != "success" {
				return nil, fmt.Errorf("workflow_failed: workflow finished with %s; inspect the linked run and check the approved bundle contains every requested name", run.Conclusion)
			}
			break
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("waiting for Actions: %w (the remote run may continue; artifacts expire after one day)", ctx.Err())
		case <-time.After(2 * time.Second):
		}
	}
	data, err = api(ctx, "GET", runPath+"/artifacts?per_page=100", nil)
	if err != nil {
		return nil, err
	}
	var listing struct {
		Artifacts []struct {
			ID      int64
			Name    string
			Expired bool
		}
	}
	if json.Unmarshal(data, &listing) != nil {
		return nil, errors.New("invalid artifact response")
	}
	var artifactID int64
	for _, artifact := range listing.Artifacts {
		if artifact.Name == "ghenv-"+requestID && !artifact.Expired {
			artifactID = artifact.ID
			break
		}
	}
	if artifactID == 0 {
		return nil, errors.New("encrypted artifact is missing or expired")
	}
	artifactPath := fmt.Sprintf("%s/artifacts/%d", prefix, artifactID)
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, cleanupErr := api(cleanupCtx, "DELETE", artifactPath, nil); cleanupErr != nil {
			fmt.Fprintln(os.Stderr, "ghenv: could not delete encrypted artifact; it expires after one day")
		}
	}()
	data, err = api(ctx, "GET", artifactPath+"/zip", nil)
	if err != nil {
		return nil, err
	}
	payload, err := readArtifact(data)
	if err != nil {
		return nil, err
	}
	values, err := Unseal(privateKey, payload, requestID)
	if err != nil {
		return nil, err
	}
	if len(values) != len(names) {
		return nil, errors.New("workflow returned an unexpected set of names")
	}
	for _, name := range names {
		value, ok := values[name]
		if !ok || strings.ContainsRune(value, 0) {
			return nil, errors.New("workflow returned an invalid environment")
		}
	}
	return values, nil
}

func readArtifact(data []byte) ([]byte, error) {
	if len(data) > maxPayload*2 {
		return nil, errors.New("artifact archive is too large")
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("invalid artifact archive")
	}
	if len(archive.File) != 1 || archive.File[0].Name != "payload.json" {
		return nil, errors.New("artifact must contain only payload.json")
	}
	file := archive.File[0]
	if file.UncompressedSize64 > maxPayload*2 {
		return nil, errors.New("artifact payload is too large")
	}
	reader, err := file.Open()
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	payload, err := io.ReadAll(io.LimitReader(reader, maxPayload*2+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > maxPayload*2 {
		return nil, errors.New("artifact payload is too large")
	}
	return payload, nil
}
