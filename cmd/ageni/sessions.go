package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/bouwerp/ageni/internal/session"
)

// runSessions handles `ageni sessions <subcommand>`. Supported:
//
//	ageni sessions list
//	ageni sessions show <id|prefix>
//	ageni sessions resume <id|prefix>     # prints a resume command line
//	ageni sessions rm <id|prefix>
//	ageni sessions dump <id|prefix> [-o file]
//
// `ageni --session <id>` is the actual resume entry point — sessions
// resume just helps the user find the right ID and prints what to run.
func runSessions(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ageni sessions <list|show|resume|rm|dump>")
		os.Exit(1)
	}
	switch args[0] {
	case "list", "ls":
		return sessionsList()
	case "show":
		if len(args) < 2 {
			return fmt.Errorf("usage: ageni sessions show <id|prefix>")
		}
		return sessionsShow(args[1])
	case "resume":
		if len(args) < 2 {
			return fmt.Errorf("usage: ageni sessions resume <id|prefix>")
		}
		return sessionsResume(args[1])
	case "rm", "delete":
		if len(args) < 2 {
			return fmt.Errorf("usage: ageni sessions rm <id|prefix>")
		}
		return sessionsRemove(args[1])
	case "dump":
		if len(args) < 2 {
			return fmt.Errorf("usage: ageni sessions dump <id|prefix> [-o file]")
		}
		return sessionsDump(args[1:])
	default:
		return fmt.Errorf("unknown sessions subcommand: %s", args[0])
	}
}

func sessionsList() error {
	all, err := session.List()
	if err != nil {
		return err
	}
	if len(all) == 0 {
		fmt.Println("No sessions yet. Just run `ageni` to start one.")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tLAST USED\tMASTER\tSUB-AGENT\tREPO")
	for _, s := range all {
		repo := s.RepoPath
		if repo == "" {
			repo = "—"
		} else if rel, err := filepath.Rel(must(os.UserHomeDir()), repo); err == nil && len(rel) < len(repo) {
			repo = "~/" + rel
		}
		master := joinSlash(s.MasterProvider, s.MasterModel)
		sub := joinSlash(s.SubagentProvider, s.SubagentModel)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			s.ID,
			humanise(s.LastUsed),
			master,
			sub,
			repo,
		)
	}
	return w.Flush()
}

func sessionsShow(prefix string) error {
	id, err := session.ResolveID(prefix)
	if err != nil {
		return err
	}
	s, err := session.Open(id)
	if err != nil {
		return err
	}
	fmt.Printf("ID:        %s\n", s.ID)
	fmt.Printf("Started:   %s\n", s.Started.Format(time.RFC3339))
	fmt.Printf("LastUsed:  %s\n", s.LastUsed.Format(time.RFC3339))
	fmt.Printf("Repo:      %s\n", or(s.RepoPath, "—"))
	fmt.Printf("Master:    %s\n", joinSlash(s.MasterProvider, s.MasterModel))
	fmt.Printf("Sub-agent: %s\n", joinSlash(s.SubagentProvider, s.SubagentModel))
	fmt.Printf("Dir:       %s\n", s.Dir)
	if entries, err := os.ReadDir(s.Dir); err == nil {
		fmt.Println("Files:")
		for _, e := range entries {
			if info, err := e.Info(); err == nil {
				fmt.Printf("  %s  %d bytes\n", e.Name(), info.Size())
			}
		}
	}
	return nil
}

func sessionsResume(prefix string) error {
	id, err := session.ResolveID(prefix)
	if err != nil {
		return err
	}
	exe, _ := os.Executable()
	if exe == "" {
		exe = "ageni"
	}
	fmt.Printf("Run: %s --session %s\n", filepath.Base(exe), id)
	return nil
}

// sessionsDump formats the resolved session's log.jsonl as a human-readable
// transcript. Output goes to stdout by default, or to -o <file> if given.
func sessionsDump(args []string) error {
	prefix := args[0]
	out := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-o", "--out":
			if i+1 >= len(args) {
				return fmt.Errorf("-o requires a path")
			}
			out = args[i+1]
			i++
		default:
			return fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	id, err := session.ResolveID(prefix)
	if err != nil {
		return err
	}
	s, err := session.Open(id)
	if err != nil {
		return err
	}
	if out == "" {
		return session.FormatLog(s, os.Stdout)
	}
	f, err := os.Create(out) //nolint:gosec
	if err != nil {
		return err
	}
	defer f.Close()
	if err := session.FormatLog(s, f); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", out)
	return nil
}

func sessionsRemove(prefix string) error {
	id, err := session.ResolveID(prefix)
	if err != nil {
		return err
	}
	root, err := session.SessionsRoot()
	if err != nil {
		return err
	}
	dir := filepath.Join(root, id)
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	fmt.Printf("Removed session %s\n", id)
	return nil
}

func humanise(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02")
	}
}

func or(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func joinSlash(a, b string) string {
	if a == "" && b == "" {
		return "—"
	}
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "/" + b
}

func must(s string, _ error) string { return s }
