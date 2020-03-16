package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"strings"
	"time"

	"github.com/brigadecore/brigade-github-app/pkg/check"
	ghlib "github.com/brigadecore/brigade-github-app/pkg/github"
	"github.com/brigadecore/brigade-github-app/pkg/webhook"
	"github.com/google/go-github/github"
)

func main() {
	payload := os.Getenv("CHECK_PAYLOAD")
	name := envOr("CHECK_NAME", "Brigade")
	title := envOr("CHECK_TITLE", "Running Check")
	summary := envOr("CHECK_SUMMARY", "")
	text := envOrFile("CHECK_TEXT", "/check-run/text", "")
	conclusion := envOr("CHECK_CONCLUSION", "")
	detailsURL := envOr("CHECK_DETAILS_URL", "")
	externalID := envOr("CHECK_EXTERNAL_ID", "")
	startedAt := envOr("CHECK_STARTED_AT", time.Now().Format(check.RFC8601))

	// Support for GH Enterprise.
	ghBaseURL := envOr("GITHUB_BASE_URL", "")
	ghUploadURL := envOr("GITHUB_UPLOAD_URL", ghBaseURL)

	var actions []check.Action
	actionsJSON := envOr("CHECK_ACTIONS", "")
	if actionsJSON != "" {
		if err := json.Unmarshal([]byte(actionsJSON), &actions); err != nil {
			fmt.Printf("Error: could not parse actions: %s\n", err)
			os.Exit(1)
		}
	}

	data := &webhook.Payload{}
	if err := json.Unmarshal([]byte(payload), data); err != nil {
		fmt.Printf("Error: could not parse payload: %s\n", err)
		os.Exit(1)
	}
	token := data.Token

	repo, commit, branch, err := repoCommitBranch(data)
	if err != nil {
		fmt.Printf("Error processing data: %s", err)
		os.Exit(2)
	}

	parts := strings.Split(repo, "/")
	if len(parts) != 2 {
		fmt.Println("Error: CheckSuite.Repository.FullName is required")
		os.Exit(1)
	}

	run := check.Run{
		Name:       name,
		HeadBranch: branch,
		HeadSHA:    commit,
		StartedAt:  startedAt,
		ExternalID: externalID,
		DetailsURL: detailsURL,
		Output: check.Output{
			Title:   title,
			Summary: summary,
			Text:    text,
		},
		Status: "in_progress",
	}

	if len(conclusion) > 0 {
		run.Conclusion = conclusion
		run.Status = "completed"
		run.CompletedAt = time.Now().Format(check.RFC8601)
	}

	if actions != nil {
		run.Actions = actions
	}

	// Once we have the token, we can switch from the app token to the
	// installation token.
	ghc, err := ghlib.NewClientFromInstallationToken(
		ghBaseURL,
		ghUploadURL,
		token,
	)
	if err != nil {
		fmt.Println(err)
		os.Exit(3)
	}
	ct := &checkTool{
		client: ghc,
		owner:  parts[0],
		repo:   parts[1],
	}

	out, err := ct.createRun(run)
	if err != nil {
		fmt.Printf("Error: %s (got %s)\n", err, out)
		os.Exit(1)
	}
	fmt.Println(out)
}

func repoCommitBranch(payload *webhook.Payload) (string, string, string, error) {
	var repo, commit, branch string
	// As ridiculous as this is, we have to remarshal the Body and unmarshal it
	// into the right object.
	tmp, err := json.Marshal(payload.Body)
	if err != nil {
		return repo, commit, branch, err
	}
	switch payload.Type {
	case "check_run":
		event := &github.CheckRunEvent{}
		if err = json.Unmarshal(tmp, event); err != nil {
			return repo, commit, branch, err
		}
		repo = event.Repo.GetFullName()
		commit = event.CheckRun.CheckSuite.GetHeadSHA()
		branch = event.CheckRun.CheckSuite.GetHeadBranch()
	case "check_suite":
		event := &github.CheckSuiteEvent{}
		if err = json.Unmarshal(tmp, event); err != nil {
			return repo, commit, branch, err
		}
		repo = event.Repo.GetFullName()
		commit = event.CheckSuite.GetHeadSHA()
		branch = event.CheckSuite.GetHeadBranch()
	case "issue_comment":
		event := &github.IssueCommentEvent{}
		if err = json.Unmarshal(tmp, event); err != nil {
			return repo, commit, branch, err
		}
		repo = event.Repo.GetFullName()
		// A github.IssueCommentEvent event does not have commit or branch fields,
		// therefore, we will expect them to be set on the payload itself
		if commit = payload.Commit; commit == "" {
			return repo, commit, branch, fmt.Errorf("commit empty")
		}
		if branch = payload.Branch; branch == "" {
			return repo, commit, branch, fmt.Errorf("branch empty")
		}
	default:
		return repo, commit, branch, fmt.Errorf("unknown payload type %s", payload.Type)
	}
	return repo, commit, branch, nil
}

type checkTool struct {
	client *github.Client
	owner  string
	repo   string
}

func (c *checkTool) createRun(cr check.Run) (string, error) {
	out := bytes.NewBuffer(nil) // FIXME

	u := fmt.Sprintf("repos/%s/%s/check-runs", c.owner, c.repo)
	req, err := c.client.NewRequest("POST", u, cr)
	if err != nil {
		return "", err
	}

	// Turn on beta feature.
	req.Header.Set("Accept", "application/vnd.github.antiope-preview+json")

	ctx := context.Background()
	res, err := c.client.Do(ctx, req, out)
	if err != nil {
		body, _ := ioutil.ReadAll(res.Body)
		res.Body.Close()
		fmt.Printf("%+v", res)

		return string(body), err
	}
	return out.String(), nil
}

func envOr(envvar, defaultText string) string {
	if val, ok := os.LookupEnv(envvar); ok {
		return val
	}
	return defaultText
}

func envOrFile(envvar, filepath, defaultText string) string {
	if val, ok := os.LookupEnv(envvar); ok {
		return val
	}
	bytes, _ := ioutil.ReadFile(filepath)
	if len(bytes) > 0 {
		return string(bytes)
	}
	return defaultText
}
