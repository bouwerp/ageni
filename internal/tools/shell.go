package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// RunBash runs a bash command and returns its combined stdout/stderr.
type RunBash struct{}

func (RunBash) Name() string { return "run_bash" }
func (RunBash) Description() string {
	return "Execute a bash command. Returns combined stdout+stderr and the exit code. Has a 60-second default timeout (configurable up to 600s)."
}
func (RunBash) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Bash command to run"},"timeout_seconds":{"type":"integer","description":"Timeout in seconds (default 60, max 600)"}},"required":["command"]}`)
}
func (RunBash) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Command        string
		TimeoutSeconds int `json:"timeout_seconds"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Command == "" {
		return "", errors.New("command is required")
	}
	if p.TimeoutSeconds <= 0 {
		p.TimeoutSeconds = 60
	}
	if p.TimeoutSeconds > 600 {
		p.TimeoutSeconds = 600
	}

	rctx, cancel := context.WithTimeout(ctx, time.Duration(p.TimeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(rctx, "bash", "-lc", p.Command)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()

	exitCode := 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			exitCode = ee.ExitCode()
		} else if rctx.Err() == context.DeadlineExceeded {
			return fmt.Sprintf("[timed out after %ds]\n%s", p.TimeoutSeconds, buf.String()), nil
		} else {
			return "", err
		}
	}
	out := buf.String()
	out = collapseBlankLines(sanitizeOutput(out))
	lines := strings.Split(out, "\n")
	if len(lines) > 160 {
		out = strings.Join(lines[:160], "\n") + "\n[truncated to 160 lines]"
	}
	if len(out) > 4096 {
		out = out[:4096] + "\n[truncated to 4KB]"
	}
	if exitCode != 0 {
		out = EnrichErrorContext(out)
	}
	return fmt.Sprintf("[exit %d]\n%s", exitCode, out), nil
}
