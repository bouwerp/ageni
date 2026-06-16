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
