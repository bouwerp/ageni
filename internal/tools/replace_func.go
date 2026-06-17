package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
)

type ReplaceFunction struct {
	Tracker *ChangeTracker
}

func (ReplaceFunction) Name() string { return "replace_function" }
func (ReplaceFunction) Description() string {
	return "Replace the entire body and signature of a specific Go function or method. Highly recommended for local models instead of apply_diff when modifying a single function. Provide the entire new function code. For methods, use Receiver.Method (e.g. Server.Start)."
}
func (ReplaceFunction) Schema() json.RawMessage {
	return json.RawMessage(`{
"type":"object",
"properties":{
  "path":{"type":"string","description":"Target Go file."},
  "function_name":{"type":"string","description":"Name of the function or Receiver.Method."},
  "new_code":{"type":"string","description":"The complete new function code, starting with 'func ...'"}
},
"required":["path", "function_name", "new_code"]
}`)
}

func (t ReplaceFunction) Call(ctx context.Context, args json.RawMessage) (string, error) {
	var p struct {
		Path         string `json:"path"`
		FunctionName string `json:"function_name"`
		NewCode      string `json:"new_code"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if p.Path == "" {
		return "", fmt.Errorf("path is required")
	}
	validatedPath, err := ValidatePath(p.Path)
	if err != nil {
		return "", err
	}
	p.Path = validatedPath

	if filepath.Ext(p.Path) != ".go" {
		return "", fmt.Errorf("replace_function only supports .go files. Use apply_diff for other languages")
	}

	src, err := os.ReadFile(p.Path)
	if err != nil {
		return "", err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, p.Path, src, 0)
	if err != nil {
		return "", fmt.Errorf("could not parse file (syntax error): %v", err)
	}

	var targetFunc *ast.FuncDecl
	for _, decl := range file.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			name := fd.Name.Name
			if fd.Recv != nil && len(fd.Recv.List) > 0 {
				var typeName string
				switch v := fd.Recv.List[0].Type.(type) {
				case *ast.Ident:
					typeName = v.Name
				case *ast.StarExpr:
					if id, ok := v.X.(*ast.Ident); ok {
						typeName = id.Name
					}
				}
				if typeName != "" {
					name = typeName + "." + name
				}
			}
			if name == p.FunctionName {
				if targetFunc != nil {
					return "", fmt.Errorf("multiple functions matched %q, replace_function cannot safely determine which one to replace", p.FunctionName)
				}
				targetFunc = fd
			}
		}
	}

	if targetFunc == nil {
		return "", fmt.Errorf("function %q not found in %s", p.FunctionName, p.Path)
	}

	start := fset.Position(targetFunc.Pos()).Offset
	end := fset.Position(targetFunc.End()).Offset

	prefix := src[:start]
	suffix := src[end:]

	newSrc := append(prefix, []byte(strings.TrimSpace(p.NewCode))...)
	newSrc = append(newSrc, suffix...)

	formatted, err := format.Source(newSrc)
	if err != nil {
		return "", fmt.Errorf("failed to format new source code (is there a syntax error in your new_code?): %v", err)
	}

	if t.Tracker != nil {
		t.Tracker.Snapshot(p.Path)
	}

	if err := os.WriteFile(p.Path, formatted, 0644); err != nil {
		return "", err
	}

	return fmt.Sprintf("Successfully replaced function %q in %s", p.FunctionName, p.Path), nil
}
