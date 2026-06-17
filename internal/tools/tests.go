package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RunTests runs a test command appropriate to the project's language and
// returns a compact summary. For Go it parses 'go test -json' output into
// pass/fail counts; for other languages it returns the raw output trimmed.
type RunTests struct{}

func (RunTests) Name() string { return "run_tests" }
func (RunTests) Description() string {
	return `Run tests. Defaults to auto-detect by repo files (go.mod → go test, package.json → npm/pnpm/yarn/bun test, pyproject.toml → pytest, Cargo.toml → cargo test, pom.xml/gradlew → Java test runners). Pass 'command' to override. Go output is parsed structurally; others return trimmed stdout/stderr.`
}
func (RunTests) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "command":{"type":"string","description":"Override the auto-detected command (full shell command)."},
  "path":{"type":"string","description":"Limit to a package/path. Defaults to ./..."},
  "timeout_seconds":{"type":"integer","description":"Default 300, max 1800."}
},
"required":[]
}`)
}
func (RunTests) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Command        string `json:"command"`
		Path           string `json:"path"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	_ = json.Unmarshal(args, &p)
	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 300
	}
	if p.TimeoutSeconds > 1800 {
		p.TimeoutSeconds = 1800
	}

	rctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()

	if p.Command != "" {
		cmd := exec.CommandContext(rctx, "bash", "-lc", p.Command)
		out, err := cmd.CombinedOutput()
		return formatTestOutput(string(out), err), nil
	}

	// Auto-detect.
	switch {
	case fileExists("go.mod"):
		return runGoTests(rctx, p.Path), nil
	case fileExists("package.json"):
		switch {
		case fileExists("pnpm-lock.yaml"):
			return runShellTest(rctx, "pnpm test --silent"), nil
		case fileExists("yarn.lock"):
			return runShellTest(rctx, "yarn test --silent"), nil
		case fileExists("bun.lockb"), fileExists("bun.lock"):
			return runShellTest(rctx, "bun test"), nil
		default:
			return runShellTest(rctx, "npm test --silent"), nil
		}
	case fileExists("pyproject.toml"), fileExists("setup.py"):
		return runShellTest(rctx, "pytest -q"), nil
	case fileExists("Cargo.toml"):
		return runShellTest(rctx, "cargo test --quiet"), nil
	case fileExists("pom.xml"):
		return runShellTest(rctx, "mvn -q test"), nil
	case fileExists("gradlew"):
		return runShellTest(rctx, "./gradlew -q test"), nil
	default:
		return "", errors.New("could not auto-detect test runner; pass 'command' explicitly")
	}
}

func runShellTest(ctx context.Context, command string) string {
	cmd := exec.CommandContext(ctx, "bash", "-lc", command)
	out, err := cmd.CombinedOutput()
	return formatTestOutput(string(out), err)
}

func runGoTests(ctx context.Context, path string) string {
	if path == "" {
		path = "./..."
	}
	cmd := exec.CommandContext(ctx, "go", "test", "-json", path)
	out, _ := cmd.Output()
	pass, fail, skip := 0, 0, 0
	var failures []string
	outputs := make(map[string][]string)
	pkgFailedTests := make(map[string]bool)
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var ev struct {
			Action  string  `json:"Action"`
			Package string  `json:"Package"`
			Test    string  `json:"Test"`
			Output  string  `json:"Output"`
			Elapsed float64 `json:"Elapsed"`
		}
		if err := dec.Decode(&ev); err != nil {
			break
		}

		key := ev.Package
		if ev.Test != "" {
			key += "." + ev.Test
		}

		if ev.Action == "output" && ev.Output != "" {
			outputs[key] = append(outputs[key], ev.Output)
		}

		if ev.Test == "" {
			if ev.Action == "fail" && !pkgFailedTests[ev.Package] {
				fail++
				failures = append(failures, fmt.Sprintf("FAIL %s (build/init)\n%s", ev.Package, strings.TrimSpace(strings.Join(outputs[key], ""))))
			}
			continue
		}

		switch ev.Action {
		case "pass":
			pass++
			delete(outputs, key)
		case "fail":
			fail++
			pkgFailedTests[ev.Package] = true
			failures = append(failures, fmt.Sprintf("FAIL %s (%.2fs)\n%s", key, ev.Elapsed, strings.TrimSpace(strings.Join(outputs[key], ""))))
		case "skip":
			skip++
			delete(outputs, key)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "go test: pass=%d fail=%d skip=%d\n", pass, fail, skip)
	for _, f := range failures {
		sb.WriteString("  " + f + "\n")
	}
	if fail == 0 && pass == 0 && skip == 0 {
		sb.WriteString("(no tests run — check path)\n")
	}
	res := strings.TrimRight(sb.String(), "\n")
	if fail > 0 {
		res = EnrichErrorContext(res)
	}
	return res
}

func formatTestOutput(out string, err error) string {
	if len(out) > 12000 {
		out = out[:12000] + "\n[truncated to 12KB]"
	}
	if err != nil {
		out = EnrichErrorContext(out)
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return fmt.Sprintf("[exit %d]\n%s", ee.ExitCode(), out)
		}
		return fmt.Sprintf("[error: %v]\n%s", err, out)
	}
	return fmt.Sprintf("[exit 0]\n%s", out)
}

func fileExists(name string) bool {
	cmd := exec.Command("test", "-e", name)
	return cmd.Run() == nil
}
