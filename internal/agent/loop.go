package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/bouwerp/ageni/internal/llm"
)

// loopDetectionWindowSize is the number of recent tool-call fingerprints kept.
const loopDetectionWindowSize = 10

// loopDetectionMaxRepeats is how many times the same fingerprint may appear
// in the window before the agent is flagged as looping.
const loopDetectionMaxRepeats = 5

// loopDetector tracks a sliding window of (tool_name + input + output)
// fingerprints and detects when the master is stuck repeating the same
// sequence of tool calls.
type loopDetector struct {
	window []string // circular fingerprint history (loopDetectionWindowSize max)
}

// record adds a fingerprint for the given tool call and its result.
// Returns a non-empty diagnostic string if a loop is detected.
func (d *loopDetector) record(call llm.ToolCall, result llm.ToolResult) string {
	fp := fingerprint(call.Name, string(call.Arguments), result.Content)

	d.window = append(d.window, fp)
	if len(d.window) > loopDetectionWindowSize {
		d.window = d.window[len(d.window)-loopDetectionWindowSize:]
	}

	// Count occurrences of this fingerprint in the window.
	count := 0
	for _, w := range d.window {
		if w == fp {
			count++
		}
	}
	if count >= loopDetectionMaxRepeats {
		return fmt.Sprintf(
			"[loop detected] tool %q with the same inputs and output has been called %d times in the last %d tool calls. "+
				"Stop repeating this call. Reassess your approach, try a different tool or strategy, or report to the user that you are stuck.",
			call.Name, count, loopDetectionWindowSize,
		)
	}
	return ""
}

// reset clears the window (call at the start of each user-message turn).
func (d *loopDetector) reset() {
	d.window = d.window[:0]
}

func fingerprint(toolName, input, output string) string {
	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte("\x00"))
	h.Write([]byte(input))
	h.Write([]byte("\x00"))
	// Cap output contribution to 512 bytes so minor trailing whitespace
	// differences don't prevent detection of a real loop.
	if len(output) > 512 {
		output = output[:512]
	}
	h.Write([]byte(output))
	return hex.EncodeToString(h.Sum(nil))
}
