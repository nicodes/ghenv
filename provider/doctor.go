package provider

import (
	"context"
	"encoding/json"
	"errors"
)

// CheckWorkflow reads workflow metadata without exporting any credentials.
func CheckWorkflow(ctx context.Context, repo string) error {
	if err := (Request{Repo: repo, Environment: "Preview", Ref: "main", Variables: "GHENV_PREFLIGHT"}).Validate(); err != nil {
		return err
	}
	data, err := ghAPI(ctx, "GET", "repos/"+repo+"/actions/workflows/ghenv.yml", nil)
	if err != nil {
		return err
	}
	var workflow struct {
		State string `json:"state"`
	}
	if json.Unmarshal(data, &workflow) != nil {
		return errors.New("invalid_workflow_response: GitHub returned invalid workflow metadata")
	}
	if workflow.State != "active" {
		return errors.New("workflow_disabled: enable .github/workflows/ghenv.yml in GitHub Actions")
	}
	return nil
}
