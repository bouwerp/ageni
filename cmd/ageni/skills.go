package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/bouwerp/ageni/internal/skills"
)

// runSkills handles `ageni skills <subcommand>`. Supported:
//
//	ageni skills list
//	ageni skills install <git-url>          (clones repo, copies skills/* to ~/.ageni/skills/)
//	ageni skills path                       (print the global skills dir)
func runSkills(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ageni skills <list|install|path>")
		os.Exit(1)
	}
	switch args[0] {
	case "list":
		return skillsList()
	case "install":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: ageni skills install <git-url>")
			os.Exit(1)
		}
		return skillsInstall(args[1])
	case "path":
		dir, err := skillsDir()
		if err != nil {
			return err
		}
		fmt.Println(dir)
		return nil
	default:
		return fmt.Errorf("unknown skills subcommand: %s", args[0])
	}
}

func skillsList() error {
	reg, err := skills.Load()
	if err != nil {
		return err
	}
	all := reg.All()
	if len(all) == 0 {
		dir, _ := skillsDir()
		fmt.Printf("No skills installed. Try: ageni skills install git@github.com:realfi-co/agent-skills.git\n(skills are loaded from %s and ./.ageni/skills/)\n", dir)
		return nil
	}
	for _, s := range all {
		fmt.Printf("%-32s v%-6s %s\n", s.Name, s.Version, firstLine(s.Description))
	}
	return nil
}

func skillsInstall(repoURL string) error {
	dest, err := skillsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dest, 0o755); err != nil { //nolint:gosec
		return err
	}

	tmp, err := os.MkdirTemp("", "ageni-skills-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	fmt.Printf("Cloning %s...\n", repoURL)
	cmd := exec.Command("git", "clone", "--depth", "1", repoURL, tmp)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("git clone failed: %w", err)
	}

	// Find the skills/ subdirectory.
	src := filepath.Join(tmp, "skills")
	if _, err := os.Stat(src); err != nil {
		// Some repos put skills at the root; try that.
		src = tmp
	}

	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}

	installed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Only treat dirs that contain a SKILL.md as skills.
		if _, err := os.Stat(filepath.Join(src, e.Name(), "SKILL.md")); err != nil {
			continue
		}
		target := filepath.Join(dest, e.Name())
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		if err := copyDir(filepath.Join(src, e.Name()), target); err != nil {
			return fmt.Errorf("copy %s: %w", e.Name(), err)
		}
		installed++
		fmt.Printf("  installed %s\n", e.Name())
	}

	if installed == 0 {
		return fmt.Errorf("no SKILL.md directories found in %s", repoURL)
	}
	fmt.Printf("\nInstalled %d skill(s) to %s\n", installed, dest)
	return nil
}

func skillsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".ageni", "skills"), nil
}

func copyDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(out, info.Mode().Perm())
		}
		data, err := os.ReadFile(path) //nolint:gosec
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil { //nolint:gosec
			return err
		}
		return os.WriteFile(out, data, info.Mode().Perm())
	})
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
