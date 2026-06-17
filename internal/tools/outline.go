package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

type ReadOutline struct{}

func (ReadOutline) Name() string { return "read_outline" }
func (ReadOutline) Description() string {
	return "Read the structural outline of a file. Returns function signatures, struct layouts, and interfaces without the implementation body. Extremely useful for exploring large files."
}
func (ReadOutline) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Absolute or relative path to the file."}
},
"required":["path"]
}`)
}

func (ReadOutline) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		p.Path = ResolvePath(args)
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	validatedPath, err := ValidatePath(p.Path)
	if err != nil {
		return "", err
	}
	p.Path = validatedPath

	if filepath.Ext(p.Path) == ".go" {
		return outlineGoFile(p.Path)
	}
	return outlineGenericFile(p.Path)
}

func outlineGoFile(path string) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		return "", err
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "// Outline of %s\npackage %s\n\n", filepath.Base(path), file.Name.Name)

	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			start := fset.Position(d.Pos()).Offset
			end := fset.Position(d.End()).Offset
			if end > len(src) {
				end = len(src)
			}
			sb.WriteString(string(src[start:end]) + "\n\n")
		case *ast.FuncDecl:
			start := fset.Position(d.Pos()).Offset
			var end int
			if d.Body != nil {
				end = fset.Position(d.Body.Lbrace).Offset
				if end > start {
					// We include the opening brace, and append " ... }"
					end++ 
				}
			} else {
				end = fset.Position(d.End()).Offset
			}
			if end > len(src) {
				end = len(src)
			}
			sig := string(src[start:end])
			if d.Body != nil {
				sb.WriteString(strings.TrimSpace(sig) + " ... }\n\n")
			} else {
				sb.WriteString(strings.TrimSpace(sig) + "\n\n")
			}
		}
	}
	if sb.Len() < 50 {
		return "", fmt.Errorf("no outline could be extracted; use read_file instead")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func outlineGenericFile(path string) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(src), "\n")
	var sb strings.Builder
	fmt.Fprintf(&sb, "// Outline of %s\n\n", filepath.Base(path))
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "class ") || strings.HasPrefix(trimmed, "function ") || strings.HasPrefix(trimmed, "export ") || strings.HasPrefix(trimmed, "interface ") || strings.HasPrefix(trimmed, "type ") || strings.HasPrefix(trimmed, "struct ") {
			sb.WriteString(fmt.Sprintf("%d: %s\n", i+1, line))
		}
	}
	if sb.Len() < 50 {
		return "", fmt.Errorf("no outline could be extracted (unsupported file type or no signatures found); use read_file instead")
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}
