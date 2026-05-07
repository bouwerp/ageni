package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
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

	if reason := checkDangerousCommand(p.Command); reason != "" {
		return "", fmt.Errorf("blocked: %s — command not executed. Rewrite the command to avoid this pattern or use a safer alternative", reason)
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
	out := truncateBashOutput(buf.String())
	return fmt.Sprintf("[exit %d]\n%s", exitCode, out), nil
}

// truncateBashOutput caps bash stdout/stderr at maxOutputChars using a
// bi-directional head+tail strategy (crush pattern): preserves the first
// half and the last half so both the initial context and the trailing error
// or result are visible even on very long outputs.
const maxOutputChars = 30_000

func truncateBashOutput(s string) string {
	if len(s) <= maxOutputChars {
		return s
	}
	half := maxOutputChars / 2
	// Snap to rune boundaries.
	headEnd := half
	for headEnd > 0 && !isRuneBoundary(s, headEnd) {
		headEnd--
	}
	tailStart := len(s) - half
	for tailStart < len(s) && !isRuneBoundary(s, tailStart) {
		tailStart++
	}
	dropped := tailStart - headEnd
	return s[:headEnd] +
		fmt.Sprintf("\n\n[... %d chars truncated ...]\n\n", dropped) +
		s[tailStart:]
}

// isRuneBoundary reports whether position i is the start of a UTF-8 code point.
func isRuneBoundary(s string, i int) bool {
	if i == 0 || i == len(s) {
		return true
	}
	return (s[i] & 0xC0) != 0x80
}

// dangerousPattern pairs a compiled regex with a human-readable reason.
type dangerousPattern struct {
	re     *regexp.Regexp
	reason string
}

// dangerousPatterns lists shell command patterns that are blocked outright.
// These represent irreversible or system-destructive operations where the
// cost of an accident far outweighs any automation benefit.
var dangerousPatterns = []dangerousPattern{
	// Recursive delete of root or home
	{re: regexp.MustCompile(`rm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?-[a-zA-Z]*r[a-zA-Z]*\s+/\s*$`), reason: "recursive delete of filesystem root (rm -rf /)"},
	{re: regexp.MustCompile(`rm\s+(-[a-zA-Z]*f[a-zA-Z]*\s+)?-[a-zA-Z]*r[a-zA-Z]*\s+~/?\s*$`), reason: "recursive delete of home directory (rm -rf ~)"},

	// Disk destruction
	{re: regexp.MustCompile(`\bmkfs\b`), reason: "mkfs formats a disk partition (data loss)"},
	{re: regexp.MustCompile(`\bdd\b.*\bof=/dev/`), reason: "dd writing to a raw device (data loss)"},
	{re: regexp.MustCompile(`\bshred\b.*\b/dev/`), reason: "shred on a raw device (data loss)"},

	// Fork bomb
	{re: regexp.MustCompile(`:\(\)\s*\{.*:\|:`), reason: "fork bomb pattern detected"},

	// Piped remote execution (curl/wget piped to shell)
	{re: regexp.MustCompile(`\b(curl|wget)\b[^|]*\|[^|]*(ba)?sh`), reason: "piping remote content directly to a shell (supply-chain risk)"},

	// Overwriting critical system files
	{re: regexp.MustCompile(`>\s*/etc/(passwd|shadow|sudoers|hosts|fstab|crontab)\b`), reason: "overwriting critical system file"},
	{re: regexp.MustCompile(`>\s*/boot/`), reason: "writing to /boot (bootloader risk)"},

	// chmod 777 on root
	{re: regexp.MustCompile(`chmod\s+-[a-zA-Z]*R[a-zA-Z]*\s+777\s+/\s*$`), reason: "chmod -R 777 / (world-writable root)"},

	// Kernel module operations
	{re: regexp.MustCompile(`\b(insmod|rmmod)\b`), reason: "loading/unloading kernel modules requires explicit approval"},
}

// checkDangerousCommand returns a non-empty reason if the command matches a
// blocked dangerous pattern. Returns "" if the command is safe to execute.
func checkDangerousCommand(command string) string {
	cmd := strings.Join(strings.Fields(command), " ")
	for _, p := range dangerousPatterns {
		if p.re.MatchString(cmd) {
			return p.reason
		}
	}
	return ""
}
