package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/bouwerp/ageni/internal/lsp"
)

// LSPDefinition finds the definition of a symbol.
type LSPDefinition struct{}

func (LSPDefinition) Name() string { return "lsp_definition" }
func (LSPDefinition) Description() string {
	return `Find the definition of a symbol at a given position (line and character) in a file. Line and character numbers are 1-indexed (matching other file read/edit tools).`
}
func (LSPDefinition) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute or relative path to the file containing the symbol."},
  "line":{"type":"integer","description":"1-indexed line number of the symbol."},
  "character":{"type":"integer","description":"1-indexed character position (column) of the symbol."}
},
"required":["path","line","character"]
}`)
}
func (LSPDefinition) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		p.Path = ResolvePath(args)
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	if p.Line <= 0 || p.Character <= 0 {
		return "", errors.New("line and character must be greater than 0")
	}

	locs, err := lsp.GlobalLSPManager.GetDefinition(ctx, p.Path, p.Line-1, p.Character-1)
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return "no definition found", nil
	}

	var sb strings.Builder
	for _, loc := range locs {
		relPath := lsp.URIToPath(loc.URI)
		fmt.Fprintf(&sb, "%s:%d:%d\n", relPath, loc.Range.Start.Line+1, loc.Range.Start.Character+1)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// LSPReferences finds all references to a symbol.
type LSPReferences struct{}

func (LSPReferences) Name() string { return "lsp_references" }
func (LSPReferences) Description() string {
	return `Find all references of a symbol at a given position (line and character) in a file. Line and character numbers are 1-indexed.`
}
func (LSPReferences) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute or relative path to the file."},
  "line":{"type":"integer","description":"1-indexed line number of the symbol."},
  "character":{"type":"integer","description":"1-indexed character position (column) of the symbol."}
},
"required":["path","line","character"]
}`)
}
func (LSPReferences) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		p.Path = ResolvePath(args)
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	if p.Line <= 0 || p.Character <= 0 {
		return "", errors.New("line and character must be greater than 0")
	}

	locs, err := lsp.GlobalLSPManager.GetReferences(ctx, p.Path, p.Line-1, p.Character-1)
	if err != nil {
		return "", err
	}
	if len(locs) == 0 {
		return "no references found", nil
	}

	var sb strings.Builder
	for _, loc := range locs {
		relPath := lsp.URIToPath(loc.URI)
		fmt.Fprintf(&sb, "%s:%d:%d\n", relPath, loc.Range.Start.Line+1, loc.Range.Start.Character+1)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// LSPHover gets type/doc info for a symbol.
type LSPHover struct{}

func (LSPHover) Name() string { return "lsp_hover" }
func (LSPHover) Description() string {
	return `Get documentation and type information for a symbol at a given position (line and character) in a file. Line and character numbers are 1-indexed.`
}
func (LSPHover) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute or relative path to the file."},
  "line":{"type":"integer","description":"1-indexed line number of the symbol."},
  "character":{"type":"integer","description":"1-indexed character position (column) of the symbol."}
},
"required":["path","line","character"]
}`)
}
func (LSPHover) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path      string `json:"path"`
		Line      int    `json:"line"`
		Character int    `json:"character"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		p.Path = ResolvePath(args)
	}
	if p.Path == "" {
		return "", errors.New("path is required")
	}
	if p.Line <= 0 || p.Character <= 0 {
		return "", errors.New("line and character must be greater than 0")
	}

	hoverInfo, err := lsp.GlobalLSPManager.GetHover(ctx, p.Path, p.Line-1, p.Character-1)
	if err != nil {
		return "", err
	}
	if hoverInfo == "" {
		return "no hover information available", nil
	}
	return hoverInfo, nil
}

// LSPDiagnostics retrieves compilation/linter errors.
type LSPDiagnostics struct{}

func (LSPDiagnostics) Name() string { return "lsp_diagnostics" }
func (LSPDiagnostics) Description() string {
	return `Retrieve compiler, linter, and type-checker warnings or errors across the workspace (or narrowed down by a directory path prefix).`
}
func (LSPDiagnostics) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path_prefix":{"type":"string","description":"Optional directory or file prefix to filter diagnostics. Default is empty (returns all workspace diagnostics)."}
},
"required":[]
}`)
}
func (LSPDiagnostics) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		PathPrefix string `json:"path_prefix"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}

	diags := lsp.GlobalLSPManager.GetDiagnostics(p.PathPrefix)
	if len(diags) == 0 {
		return "no diagnostics found", nil
	}

	var sb strings.Builder
	for _, d := range diags {
		sev := "ERROR"
		switch d.Severity {
		case lsp.SeverityWarning:
			sev = "WARNING"
		case lsp.SeverityInformation:
			sev = "INFO"
		case lsp.SeverityHint:
			sev = "HINT"
		}
		fmt.Fprintf(&sb, "[%s] Line %d:%d - %s (Source: %s)\n", sev, d.Range.Start.Line+1, d.Range.Start.Character+1, d.Message, d.Source)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
