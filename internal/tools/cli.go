package tools

import (
	"fmt"
	"os/exec"
	"runtime"
)

// requireCLI returns a friendly error when an external CLI tool isn't
// installed, with platform-specific install hints.
func requireCLI(name string) error {
	if _, err := exec.LookPath(name); err == nil {
		return nil
	}
	hint := installHint(name)
	if hint == "" {
		return fmt.Errorf("required CLI %q not found on PATH", name)
	}
	return fmt.Errorf("required CLI %q not found on PATH. Install it with: %s", name, hint)
}

func installHint(name string) string {
	switch runtime.GOOS {
	case "darwin":
		switch name {
		case "rg":
			return "brew install ripgrep"
		case "gh":
			return "brew install gh"
		case "git":
			return "brew install git"
		}
	case "linux":
		switch name {
		case "rg":
			return "apt-get install -y ripgrep  # or: dnf install ripgrep / pacman -S ripgrep"
		case "gh":
			return "https://github.com/cli/cli#installation"
		case "git":
			return "apt-get install -y git  # or: dnf install git / pacman -S git"
		}
	case "windows":
		switch name {
		case "rg":
			return "winget install BurntSushi.ripgrep.MSVC  # or: choco install ripgrep"
		case "gh":
			return "winget install GitHub.cli  # or: choco install gh"
		case "git":
			return "winget install Git.Git"
		}
	}
	return ""
}

// CLIDep describes an external CLI tool ageni shells out to.
type CLIDep struct {
	Name        string
	Description string
	Required    bool // some tools require it; some are optional
}

// KnownCLIDeps are the external tools ageni's built-in toolset shells out to.
// `ageni doctor` checks each.
var KnownCLIDeps = []CLIDep{
	{Name: "rg", Description: "ripgrep — fast code search", Required: true},
	{Name: "git", Description: "git — diff, status, log", Required: true},
	{Name: "gh", Description: "GitHub CLI — PRs, issues, code search", Required: false},
}
