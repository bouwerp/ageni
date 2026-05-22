package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRunBashCollapsesBlankLinesAndTruncates(t *testing.T) {
	tool := RunBash{}
	args, _ := json.Marshal(map[string]any{
		"command": `for i in $(seq 1 220); do
  printf 'line-%03d\n' "$i"
  if [ "$i" = "3" ]; then printf '\n\n\n\n'; fi
done`,
	})
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatalf("RunBash: %v", err)
	}
	if strings.Contains(out, "\n\n\n\n") {
		t.Fatalf("expected blank lines to be collapsed, got: %q", out)
	}
	if !strings.Contains(out, "[truncated to 160 lines]") {
		t.Fatalf("expected line truncation notice, got: %s", out)
	}
}
