package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type FindReferences struct{}

type referenceMatch struct {
	Path    string
	Line    int
	Column  int
	Snippet string
}

type goSymbolMatch struct {
	Path  string
	Line  int
	Name  string
	Kind  string
	score int
}

func (FindReferences) Name() string { return "find_references" }

func (FindReferences) Description() string {
	return "Find exact Go identifier references using AST parsing instead of raw text search. Returns file, line, column, and source snippet."
}

func (FindReferences) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "symbol":{"type":"string","description":"Exact Go identifier name to search for."},
  "path_prefix":{"type":"string","description":"Optional relative subdirectory/file prefix to narrow the search."},
  "limit":{"type":"integer","description":"Maximum matches to return. Default 50."}
},
"required":["symbol"]
}`)
}

func (FindReferences) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Symbol     string `json:"symbol"`
		PathPrefix string `json:"path_prefix"`
		Limit      int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	p.Symbol = strings.TrimSpace(p.Symbol)
	if p.Symbol == "" {
		return "", fmt.Errorf("symbol is required")
	}
	if p.Limit <= 0 {
		p.Limit = 50
	}
	root, err := os.Getwd()
	if err != nil {
		return "", err
	}
	matches, err := findGoReferences(ctx, root, p.Symbol, p.PathPrefix, p.Limit)
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "no references found", nil
	}
	var sb strings.Builder
	for _, m := range matches {
		fmt.Fprintf(&sb, "--- %s:%d:%d ---\n%s\n", m.Path, m.Line, m.Column, strings.TrimRight(m.Snippet, "\n"))
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

func findGoReferences(ctx context.Context, root, symbol, pathPrefix string, limit int) ([]referenceMatch, error) {
	files, err := collectGoReferenceFiles(root, pathPrefix)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	matches := make([]referenceMatch, 0, min(limit, len(files)))
	for _, path := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		src, err := os.ReadFile(path) //nolint:gosec
		if err != nil {
			return nil, err
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			continue
		}
		lines := strings.Split(string(src), "\n")
		ast.Inspect(file, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != symbol {
				return true
			}
			pos := fset.Position(ident.Pos())
			if pos.Line <= 0 || pos.Line > len(lines) {
				return true
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				rel = path
			}
			startLine := pos.Line - 3
			if startLine < 0 {
				startLine = 0
			}
			endLine := pos.Line + 2
			if endLine > len(lines) {
				endLine = len(lines)
			}
			var snippetSb strings.Builder
			for i := startLine; i < endLine; i++ {
				prefix := "  "
				if i == pos.Line-1 {
					prefix = "> "
				}
				snippetSb.WriteString(fmt.Sprintf("%s%d: %s\n", prefix, i+1, lines[i]))
			}
			matches = append(matches, referenceMatch{
				Path:    rel,
				Line:    pos.Line,
				Column:  pos.Column,
				Snippet: snippetSb.String(),
			})
			return len(matches) < limit
		})
		if len(matches) >= limit {
			break
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		if matches[i].Line != matches[j].Line {
			return matches[i].Line < matches[j].Line
		}
		return matches[i].Column < matches[j].Column
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func collectGoReferenceFiles(root, pathPrefix string) ([]string, error) {
	prefix := strings.TrimSpace(pathPrefix)
	if prefix != "" {
		prefix = filepath.Clean(prefix)
		if prefix == "." {
			prefix = ""
		}
	}
	files := make([]string, 0, 64)
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "dist", "build", "target":
				return filepath.SkipDir
			}
			if prefix != "" && rel != "." && !strings.HasPrefix(rel, prefix) && !strings.HasPrefix(prefix, rel+string(filepath.Separator)) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		if prefix != "" && !strings.HasPrefix(rel, prefix) {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func searchGoSymbols(ctx context.Context, root, query string, limit int) ([]goSymbolMatch, error) {
	files, err := collectGoReferenceFiles(root, "")
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	lq := strings.ToLower(strings.TrimSpace(query))
	if lq == "" {
		return nil, nil
	}
	fset := token.NewFileSet()
	matches := make([]goSymbolMatch, 0, min(limit, len(files)))
	add := func(path, name, kind string, line int) {
		score := 0
		ln := strings.ToLower(name)
		switch {
		case ln == lq:
			score = 300
		case strings.HasPrefix(ln, lq):
			score = 200
		case strings.Contains(ln, lq):
			score = 100
		default:
			return
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			rel = path
		}
		matches = append(matches, goSymbolMatch{
			Path:  rel,
			Line:  line,
			Name:  name,
			Kind:  kind,
			score: score,
		})
	}
	for _, path := range files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			continue
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				kind := "func"
				if d.Recv != nil {
					kind = "method"
				}
				add(path, d.Name.Name, kind, fset.Position(d.Name.Pos()).Line)
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						add(path, s.Name.Name, "type", fset.Position(s.Name.Pos()).Line)
					case *ast.ValueSpec:
						kind := strings.ToLower(d.Tok.String())
						for _, name := range s.Names {
							add(path, name.Name, kind, fset.Position(name.Pos()).Line)
						}
					}
				}
			}
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].score != matches[j].score {
			return matches[i].score > matches[j].score
		}
		if matches[i].Path != matches[j].Path {
			return matches[i].Path < matches[j].Path
		}
		if matches[i].Name != matches[j].Name {
			return matches[i].Name < matches[j].Name
		}
		return matches[i].Line < matches[j].Line
	})
	if len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}
