package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// Grep runs ripgrep with --json output and returns matches as a compact
// path:line:col text block.
type Grep struct{}

func (Grep) Name() string { return "grep" }
func (Grep) Description() string {
	return `Search for a regex pattern across files using ripgrep. Returns matches as 'path:line:col: text' lines, capped at 50 hits by default. Faster and more accurate than grepping with run_bash. Pass type=go|py|js|... to restrict by language.`
}
func (Grep) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "pattern":{"type":"string","description":"Regex pattern (Rust regex syntax). Plain strings work too."},
  "path":{"type":"string","description":"Directory or file to search. Defaults to cwd."},
  "type":{"type":"string","description":"Restrict by ripgrep type, e.g. go, py, js, ts, md, yaml. Optional."},
  "case_sensitive":{"type":"boolean","description":"Defaults to smart-case (case-insensitive unless pattern contains uppercase)."},
  "max_results":{"type":"integer","description":"Cap on returned matches. Default 50, max 200."}
},
"required":["pattern"]
}`)
}
func (Grep) Call(ctx context.Context, args json.RawMessage) (string, error) {
	if err := requireCLI("rg"); err != nil {
		return "", err
	}
	var p struct {
		Pattern       string `json:"pattern"`
		Path          string `json:"path"`
		Type          string `json:"type"`
		CaseSensitive bool   `json:"case_sensitive"`
		MaxResults    int    `json:"max_results"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Pattern == "" {
		return "", errors.New("pattern is required")
	}
	if p.Path == "" {
		p.Path = "."
	}
	if p.MaxResults <= 0 {
		p.MaxResults = 50
	}
	if p.MaxResults > 200 {
		p.MaxResults = 200
	}

	cliArgs := []string{"--json", "--max-columns=300", "--max-columns-preview"}
	if !p.CaseSensitive {
		cliArgs = append(cliArgs, "--smart-case")
	}
	if p.Type != "" {
		cliArgs = append(cliArgs, "-t", p.Type)
	}
	cliArgs = append(cliArgs, "-e", p.Pattern, p.Path)

	cmd := exec.CommandContext(ctx, "rg", cliArgs...)
	out, _ := cmd.Output() // exit 1 = no matches; treat as empty

	var sb strings.Builder
	hits := 0
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var ev rgEvent
		if err := dec.Decode(&ev); err != nil {
			break
		}
		if ev.Type != "match" {
			continue
		}
		path := ev.Data.Path.Text
		line := ev.Data.LineNumber
		text := ev.Data.Lines.Text
		text = strings.TrimRight(text, "\r\n")
		col := 0
		if len(ev.Data.Submatches) > 0 {
			col = ev.Data.Submatches[0].Start
		}
		sb.WriteString(fmt.Sprintf("%s:%d:%d: %s\n", path, line, col, text))
		hits++
		if hits >= p.MaxResults {
			sb.WriteString(fmt.Sprintf("[truncated to %d hits]\n", p.MaxResults))
			break
		}
	}
	if hits == 0 {
		return "(no matches)", nil
	}
	return sb.String(), nil
}

// rgEvent is a subset of ripgrep's --json event schema.
type rgEvent struct {
	Type string `json:"type"`
	Data struct {
		Path struct {
			Text string `json:"text"`
		} `json:"path"`
		LineNumber int `json:"line_number"`
		Lines      struct {
			Text string `json:"text"`
		} `json:"lines"`
		Submatches []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"submatches"`
	} `json:"data"`
}
