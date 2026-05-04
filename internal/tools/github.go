package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// GitHub shells out to the `gh` CLI for repo / PR / issue / code-search
// operations. Auth lives in `gh`'s own state so we don't duplicate it.
type GitHub struct{}

func (GitHub) Name() string { return "github" }
func (GitHub) Description() string {
	return `Access GitHub via the gh CLI. Actions:
- pr_view: read a PR (action=pr_view, repo=owner/name, number=123)
- pr_list: list PRs (action=pr_list, repo=owner/name, state=open|closed|merged|all)
- pr_diff: PR diff (action=pr_diff, repo=owner/name, number=123)
- issue_view / issue_list: same shape as PRs
- code_search: search public code (action=code_search, query="...")
- repo_view: repo metadata (action=repo_view, repo=owner/name)
- run: pass a raw 'gh' subcommand (action=run, args=["pr","comment",...])

Requires the 'gh' CLI on PATH and a logged-in user (gh auth login).`
}
func (GitHub) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "action":{"type":"string","enum":["pr_view","pr_list","pr_diff","issue_view","issue_list","code_search","repo_view","run"]},
  "repo":{"type":"string","description":"owner/name. Required for most actions; defaults to current repo if omitted."},
  "number":{"type":"integer","description":"PR or issue number."},
  "state":{"type":"string","enum":["open","closed","merged","all"]},
  "query":{"type":"string","description":"For code_search."},
  "args":{"type":"array","items":{"type":"string"},"description":"For action=run, raw gh argv."},
  "limit":{"type":"integer","description":"Max items for list actions. Default 20."}
},
"required":["action"]
}`)
}
func (GitHub) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireCLI("gh"); err != nil {
		return "", err
	}
	var p struct {
		Action string   `json:"action"`
		Repo   string   `json:"repo"`
		Number int      `json:"number"`
		State  string   `json:"state"`
		Query  string   `json:"query"`
		Args   []string `json:"args"`
		Limit  int      `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Limit <= 0 {
		p.Limit = 20
	}
	if p.Limit > 100 {
		p.Limit = 100
	}

	var cliArgs []string
	switch p.Action {
	case "pr_view":
		if p.Number == 0 {
			return "", errors.New("number is required")
		}
		cliArgs = []string{"pr", "view", fmt.Sprint(p.Number)}
		if p.Repo != "" {
			cliArgs = append(cliArgs, "--repo", p.Repo)
		}
	case "pr_list":
		cliArgs = []string{"pr", "list", "--limit", fmt.Sprint(p.Limit)}
		if p.Repo != "" {
			cliArgs = append(cliArgs, "--repo", p.Repo)
		}
		if p.State != "" {
			cliArgs = append(cliArgs, "--state", p.State)
		}
	case "pr_diff":
		if p.Number == 0 {
			return "", errors.New("number is required")
		}
		cliArgs = []string{"pr", "diff", fmt.Sprint(p.Number)}
		if p.Repo != "" {
			cliArgs = append(cliArgs, "--repo", p.Repo)
		}
	case "issue_view":
		if p.Number == 0 {
			return "", errors.New("number is required")
		}
		cliArgs = []string{"issue", "view", fmt.Sprint(p.Number)}
		if p.Repo != "" {
			cliArgs = append(cliArgs, "--repo", p.Repo)
		}
	case "issue_list":
		cliArgs = []string{"issue", "list", "--limit", fmt.Sprint(p.Limit)}
		if p.Repo != "" {
			cliArgs = append(cliArgs, "--repo", p.Repo)
		}
		if p.State != "" {
			cliArgs = append(cliArgs, "--state", p.State)
		}
	case "code_search":
		if p.Query == "" {
			return "", errors.New("query is required")
		}
		cliArgs = []string{"search", "code", p.Query, "--limit", fmt.Sprint(p.Limit)}
	case "repo_view":
		if p.Repo == "" {
			return "", errors.New("repo is required")
		}
		cliArgs = []string{"repo", "view", p.Repo}
	case "run":
		if len(p.Args) == 0 {
			return "", errors.New("args is required for action=run")
		}
		cliArgs = p.Args
	default:
		return "", fmt.Errorf("unknown action: %s", p.Action)
	}

	cmd := exec.CommandContext(ctx, "gh", cliArgs...)
	out, err := cmd.CombinedOutput()
	s := strings.TrimRight(string(out), "\n")
	if err != nil {
		return "", fmt.Errorf("gh %s: %w\n%s", strings.Join(cliArgs, " "), err, s)
	}
	if len(s) > 16000 {
		s = s[:16000] + "\n[truncated to 16KB]"
	}
	return s, nil
}
