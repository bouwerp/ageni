package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get current working directory: %v", err)
	}

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{
			name:    "current directory dot",
			input:   ".",
			wantErr: false,
		},
		{
			name:    "file in current directory",
			input:   "somefile.txt",
			wantErr: false,
		},
		{
			name:    "nested file",
			input:   filepath.Join("some", "nested", "file.go"),
			wantErr: false,
		},
		{
			name:    "parent directory escaping",
			input:   "..",
			wantErr: true,
		},
		{
			name:    "grandparent directory escaping",
			input:   "../..",
			wantErr: true,
		},
		{
			name:    "root directory escaping absolute",
			input:   "/",
			wantErr: true,
		},
		{
			name:    "absolute path under cwd",
			input:   filepath.Join(cwd, "file.txt"),
			wantErr: false,
		},
		{
			name:    "temp directory path",
			input:   filepath.Join(os.TempDir(), "testfile.txt"),
			wantErr: false,
		},
		{
			name:    "system library path",
			input:   "/usr/lib/libc.so",
			wantErr: false,
		},
		{
			name:    "etc system config path",
			input:   "/etc/hosts",
			wantErr: false,
		},
		{
			name:    "random non-legitimate absolute path",
			input:   "/home/code/random_outside_path.txt",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidatePath(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePath(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if !filepath.IsAbs(got) {
					t.Errorf("ValidatePath(%q) got %q, expected absolute path", tt.input, got)
				}
				// Verify it has cwd, temp dir, or one of standard system prefixes
				isExpectedPath := strings.HasPrefix(got, cwd) ||
					strings.HasPrefix(got, os.TempDir()) ||
					strings.HasPrefix(got, "/tmp") ||
					strings.HasPrefix(got, "/usr/") ||
					strings.HasPrefix(got, "/lib/") ||
					strings.HasPrefix(got, "/lib64/") ||
					strings.HasPrefix(got, "/etc/") ||
					strings.HasPrefix(got, "/opt/")
				if !isExpectedPath {
					t.Errorf("ValidatePath(%q) got %q, which was not expected", tt.input, got)
				}
			}
		})
	}
}

func TestCleanAndMapPath(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}

	// Calculate a parent and sibling repo path
	parent := filepath.Dir(cwd)
	siblingRepo := filepath.Join(parent, "agenitest")
	siblingPath := filepath.Join(siblingRepo, "config.yaml")

	// We expect siblingPath to map to config.yaml under CWD
	expected := filepath.Join(cwd, "config.yaml")

	got := CleanAndMapPath(siblingPath)
	if got != expected {
		t.Errorf("CleanAndMapPath(%q) = %q, expected %q", siblingPath, got, expected)
	}

	// Test nested file mapping
	siblingNestedPath := filepath.Join(siblingRepo, "src", "config.py")
	expectedNested := filepath.Join(cwd, "src", "config.py")
	gotNested := CleanAndMapPath(siblingNestedPath)
	if gotNested != expectedNested {
		t.Errorf("CleanAndMapPath(%q) = %q, expected %q", siblingNestedPath, gotNested, expectedNested)
	}

	// Non-mapping case: normal file under CWD
	normalPath := filepath.Join(cwd, "src", "app.py")
	gotNormal := CleanAndMapPath(normalPath)
	if gotNormal != normalPath {
		t.Errorf("CleanAndMapPath(%q) = %q, expected no change", normalPath, gotNormal)
	}
}

func TestResolveContent(t *testing.T) {
	tests := []struct {
		name     string
		args     string
		expected string
	}{
		{
			name:     "content key exact",
			args:     `{"content": "hello world"}`,
			expected: "hello world",
		},
		{
			name:     "content key capital C",
			args:     `{"Content": "capital C content"}`,
			expected: "capital C content",
		},
		{
			name:     "text key",
			args:     `{"text": "some text"}`,
			expected: "some text",
		},
		{
			name:     "search and replace keys",
			args:     `{"search": "find me", "replace": "replace me"}`,
			expected: "<<<<<<< SEARCH\nfind me\n=======\nreplace me\n>>>>>>> REPLACE\n",
		},
		{
			name:     "SEARCH and REPLACE keys uppercase",
			args:     `{"SEARCH": "find upper", "REPLACE": "replace upper"}`,
			expected: "<<<<<<< SEARCH\nfind upper\n=======\nreplace upper\n>>>>>>> REPLACE\n",
		},
		{
			name:     "old_string and new_string keys",
			args:     `{"old_string": "old code", "new_string": "new code"}`,
			expected: "<<<<<<< SEARCH\nold code\n=======\nnew code\n>>>>>>> REPLACE\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveContent([]byte(tt.args))
			if got != tt.expected {
				t.Errorf("ResolveContent(%s) = %q, expected %q", tt.args, got, tt.expected)
			}
		})
	}
}

func TestResolveOldNewStrings(t *testing.T) {
	tests := []struct {
		name      string
		args      string
		wantOld   string
		wantNew   string
		wantFound bool
	}{
		{
			name:      "exact keys",
			args:      `{"old_string": "foo", "new_string": "bar"}`,
			wantOld:   "foo",
			wantNew:   "bar",
			wantFound: true,
		},
		{
			name:      "capital keys",
			args:      `{"OLD": "foo", "NEW": "bar"}`,
			wantOld:   "foo",
			wantNew:   "bar",
			wantFound: true,
		},
		{
			name:      "find / replacement keys",
			args:      `{"find": "foo", "replacement": "bar"}`,
			wantOld:   "foo",
			wantNew:   "bar",
			wantFound: true,
		},
		{
			name:      "missing key",
			args:      `{"old_string": "foo"}`,
			wantOld:   "foo",
			wantNew:   "",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oldStr, newStr, found := ResolveOldNewStrings([]byte(tt.args))
			if oldStr != tt.wantOld || newStr != tt.wantNew || found != tt.wantFound {
				t.Errorf("ResolveOldNewStrings(%s) = (%q, %q, %t), expected (%q, %q, %t)",
					tt.args, oldStr, newStr, found, tt.wantOld, tt.wantNew, tt.wantFound)
			}
		})
	}
}

