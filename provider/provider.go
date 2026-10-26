// Package provider implements the Preview credential handoff shared by ghenv and project runners.
package provider

import (
	"context"
	"errors"
	"strings"
)

// Validate checks the caller-selected repository, Preview scope and exact names.
func (r Request) Validate() error {
	_, repo, _ := strings.Cut(r.Repo, "/")
	if !repoName.MatchString(r.Repo) || repo == "." || repo == ".." {
		return errors.New("repository must be OWNER/REPO")
	}
	if r.Environment != "Preview" {
		return errors.New("local runs only support the Preview environment; production is refused")
	}
	if r.Ref == "" {
		return errors.New("a trusted workflow ref is required")
	}
	_, err := requestedNames(r.Secrets, r.Variables)
	return err
}

// Fetch obtains the explicit selection with the caller's GitHub CLI identity.
// Plaintext remains in process memory; the remote artifact is deleted after use.
func Fetch(ctx context.Context, request Request) (map[string]string, error) {
	if err := request.Validate(); err != nil {
		return nil, err
	}
	return fetchEnvironment(ctx, ghAPI, request)
}

// Prepare validates project launch policy and returns an isolated child environment.
// The provider uses the original parent credentials only for the remote handoff.
func Prepare(ctx context.Context, profile, mode string, base []string) ([]string, error) {
	p, err := readProject(profile)
	if err != nil {
		return nil, err
	}
	return projectEnvironment(ctx, p, mode, base, Fetch)
}

// SealBundle exports only explicitly requested values from the owner-curated bundle.
// The workflow's trusted repository identity is checked before encryption.
func SealBundle(data []byte, repository, secrets, variables, publicKey, requestID string) ([]byte, error) {
	bundle, err := parsePreviewBundle(data, repository)
	if err != nil {
		return nil, err
	}
	values, err := selectValues(bundle.Secrets, bundle.Variables, secrets, variables)
	if err != nil {
		return nil, err
	}
	if !requestName.MatchString(requestID) {
		return nil, errors.New("invalid request ID")
	}
	return Seal(publicKey, values, requestID)
}
