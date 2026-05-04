package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/bouwerp/ageni/internal/tools"
)

// runDoctor checks for required external CLI dependencies and offers to
// install missing ones via the platform package manager.
func runDoctor(autoInstall bool) error {
	fmt.Println("ageni doctor — checking external CLI dependencies")
	fmt.Println()

	missing := []tools.CLIDep{}
	for _, dep := range tools.KnownCLIDeps {
		if _, err := exec.LookPath(dep.Name); err == nil {
			fmt.Printf("  [ok]   %s — %s\n", dep.Name, dep.Description)
			continue
		}
		marker := "[warn]"
		if dep.Required {
			marker = "[MISS]"
		}
		fmt.Printf("  %s %s — %s\n", marker, dep.Name, dep.Description)
		missing = append(missing, dep)
	}
	fmt.Println()

	if len(missing) == 0 {
		fmt.Println("All dependencies satisfied.")
		return nil
	}

	cmd := platformInstallCommand(missing)
	if cmd == "" {
		fmt.Println("Manual install required for this platform:")
		for _, dep := range missing {
			fmt.Printf("  %s — see https://github.com/bouwerp/ageni#dependencies\n", dep.Name)
		}
		return nil
	}

	fmt.Printf("Suggested install command for %s:\n", runtime.GOOS)
	fmt.Printf("  %s\n", cmd)
	fmt.Println()

	install := autoInstall
	if !install && isInteractive() {
		fmt.Print("Run it now? [y/N] ")
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.ToLower(strings.TrimSpace(ans))
		install = ans == "y" || ans == "yes"
	}

	if !install {
		fmt.Println("Skipped.")
		return nil
	}

	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return nil
	}
	c := exec.Command(parts[0], parts[1:]...)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	c.Stdin = os.Stdin
	if err := c.Run(); err != nil {
		return fmt.Errorf("install command failed: %w", err)
	}

	fmt.Println()
	fmt.Println("Re-checking...")
	for _, dep := range missing {
		if _, err := exec.LookPath(dep.Name); err == nil {
			fmt.Printf("  [ok]   %s\n", dep.Name)
		} else {
			fmt.Printf("  [MISS] %s — still missing\n", dep.Name)
		}
	}
	return nil
}

// platformInstallCommand returns a single command that installs all missing
// deps via the platform's primary package manager. Returns "" if the platform
// has no known auto-install path.
func platformInstallCommand(missing []tools.CLIDep) string {
	pkgs := make([]string, 0, len(missing))
	for _, dep := range missing {
		switch dep.Name {
		case "rg":
			pkgs = append(pkgs, "ripgrep")
		default:
			pkgs = append(pkgs, dep.Name)
		}
	}
	pkgList := strings.Join(pkgs, " ")

	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("brew"); err == nil {
			return "brew install " + pkgList
		}
	case "linux":
		if _, err := exec.LookPath("apt-get"); err == nil {
			return "sudo apt-get install -y " + pkgList
		}
		if _, err := exec.LookPath("dnf"); err == nil {
			return "sudo dnf install -y " + pkgList
		}
		if _, err := exec.LookPath("yum"); err == nil {
			return "sudo yum install -y " + pkgList
		}
		if _, err := exec.LookPath("pacman"); err == nil {
			return "sudo pacman -S --noconfirm " + pkgList
		}
		if _, err := exec.LookPath("apk"); err == nil {
			return "sudo apk add " + pkgList
		}
	case "windows":
		if _, err := exec.LookPath("winget"); err == nil {
			// winget needs separate calls per package with different IDs; skip.
			return ""
		}
	}
	return ""
}

func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}
