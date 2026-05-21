package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/bouwerp/ageni/internal/repomap"
)

type SearchSymbols struct{}

func (SearchSymbols) Name() string { return "search_symbols" }
func (SearchSymbols) Description() string {
	return "Search code symbols using ctags-backed structural indexing instead of raw text grep. Returns symbol kind, file, and line."
}
func (SearchSymbols) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "query":{"type":"string","description":"Symbol name or fragment to search for."},
  "limit":{"type":"integer","description":"Maximum matches to return. Default 20."}
},
"required":["query"]
}`)
}
func (SearchSymbols) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	if goMatches, err := searchGoSymbols(ctx, root, p.Query, p.Limit); err != nil {
		return "", err
	} else if len(goMatches) > 0 {
		var sb strings.Builder
		for _, m := range goMatches {
			kind := m.Kind
			if kind == "" {
				kind = "symbol"
			}
			fmt.Fprintf(&sb, "%s:%d  %s %s\n", m.Path, m.Line, kind, m.Name)
		}
		return strings.TrimRight(sb.String(), "\n"), nil
	}
	matches, err := repomap.SearchSymbols(ctx, root, p.Query, p.Limit)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "no matching symbols found", nil
	}
	var sb strings.Builder
	for _, m := range matches {
		kind := m.Kind
		if kind == "" {
			kind = "symbol"
		}
		fmt.Fprintf(&sb, "%s:%d  %s %s\n", m.Path, m.Line, kind, m.Name)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
