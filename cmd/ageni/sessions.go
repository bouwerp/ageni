package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bouwerp/ageni/internal/homedir"
	"github.com/bouwerp/ageni/internal/session"
	"github.com/bouwerp/ageni/internal/tools"
)

// runSessions handles `ageni sessions <subcommand>`. Supported:
//
//	ageni sessions list
//	ageni sessions show <id|prefix>
//	ageni sessions resume <id|prefix>     # prints a resume command line
//	ageni sessions rm <id|prefix>
//	ageni sessions dump <id|prefix> [-o file]
//	ageni sessions changes <id|prefix>
//	ageni sessions diff <id|prefix> [path] [-o file]
//	ageni sessions checkpoints <id|prefix>
//	ageni sessions rewind <id|prefix> <step>
//
// `ageni --session <id>` is the actual resume entry point — sessions
// resume just helps the user find the right ID and prints what to run.
func runSessions(args []string) error {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: ageni sessions <list|show|resume|rm|dump|changes|diff|checkpoints|rewind>")
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
	case "changes":
		if len(args) < 2 {
			return fmt.Errorf("usage: ageni sessions changes <id|prefix>")
		}
		return sessionsChanges(args[1])
	case "diff":
		if len(args) < 2 {
			return fmt.Errorf("usage: ageni sessions diff <id|prefix> [path] [-o file]")
		}
		return sessionsDiff(args[1:])
	case "checkpoints":
		if len(args) < 2 {
			return fmt.Errorf("usage: ageni sessions checkpoints <id|prefix>")
		}
		return sessionsCheckpoints(args[1])
	case "rewind":
		if len(args) < 3 {
			return fmt.Errorf("usage: ageni sessions rewind <id|prefix> <step>")
		}
		return sessionsRewind(args[1], args[2])
	default:
		return fmt.Errorf("unknown sessions subcommand: %s", args[0])
	}
}

// sessionsCheckpoints lists every per-step checkpoint with its changes.
// Used to find the step number to rewind to.
func sessionsCheckpoints(prefix string) error {
	id, err := session.ResolveID(prefix)
	if err != nil {
		return err
	}
	s, err := session.Open(id)
	if err != nil {
		return err
	}
	tr := tools.NewChangeTracker(s.Path("changes.jsonl"), s.Path("snapshots"))
	cps := tr.Checkpoints()
	if len(cps) == 0 {
		fmt.Println("(no checkpoints — session has no per-step records)")
		return nil
	}
	cwd, _ := os.Getwd()
	for _, cp := range cps {
		fmt.Printf("step %d  %s\n", cp.Step, humanise(cp.At))
		for _, c := range cp.Changes {
			display := c.Path
			if cwd != "" {
				if rel, err := filepath.Rel(cwd, c.Path); err == nil && !strings.HasPrefix(rel, "..") {
					display = rel
				}
			}
			fmt.Printf("    %-8s %s\n", c.Kind, display)
		}
	}
	fmt.Println("\nUse `ageni sessions rewind <id> <step>` to roll back the workspace to before that step.")
	return nil
}

// sessionsRewind restores the workspace to the state before the given
// step. The session log + master message buffer aren't rewound — only
// the on-disk files. Forensic-only: future ageni run on the same
// session will still see the prior conversation in log.jsonl.
func sessionsRewind(prefix, stepStr string) error {
	step, err := strconv.Atoi(stepStr)
	if err != nil || step <= 0 {
		return fmt.Errorf("invalid step %q (must be a positive integer)", stepStr)
	}
	id, err := session.ResolveID(prefix)
	if err != nil {
		return err
	}
	s, err := session.Open(id)
	if err != nil {
		return err
	}
	tr := tools.NewChangeTracker(s.Path("changes.jsonl"), s.Path("snapshots"))
	touched, err := tr.Rewind(step)
	if err != nil {
		return err
	}
	if len(touched) == 0 {
		fmt.Printf("Nothing to rewind: no changes recorded at step %d or later.\n", step)
		return nil
	}
	fmt.Printf("Rewound to before step %d. Touched %d path(s):\n", step, len(touched))
	cwd, _ := os.Getwd()
	for _, p := range touched {
		display := p
		if cwd != "" {
			if rel, err := filepath.Rel(cwd, p); err == nil && !strings.HasPrefix(rel, "..") {
				display = rel
			}
		}
		fmt.Println("  -", display)
	}
	fmt.Println("\nNote: the conversation log + master message buffer are NOT rewound.")
	fmt.Println("If you resume the session, the master will see its prior history but the")
	fmt.Println("workspace state below those edits is now gone.")
	return nil
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
		} else if home, herr := homedir.Dir(); herr == nil {
			if rel, err := filepath.Rel(home, repo); err == nil && len(rel) < len(repo) {
				repo = "~/" + rel
			}
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

// sessionsChanges prints a one-line-per-path summary of every recorded
// file mutation in the session.
func sessionsChanges(prefix string) error {
	id, err := session.ResolveID(prefix)
	if err != nil {
		return err
	}
	s, err := session.Open(id)
	if err != nil {
		return err
	}
	return session.FormatChanges(s, os.Stdout)
}

// sessionsDiff prints a unified diff for the session's recorded changes.
// With no path arg, every changed file is diffed; with a path, only that
// file. Output to stdout, or -o <file>.
func sessionsDiff(args []string) error {
	prefix := args[0]
	out := ""
	path := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "-o", "--out":
			if i+1 >= len(args) {
				return fmt.Errorf("-o requires a path")
			}
			out = args[i+1]
			i++
		default:
			if strings.HasPrefix(args[i], "-") {
				return fmt.Errorf("unknown flag: %s", args[i])
			}
			path = args[i]
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
		return session.FormatDiff(s, path, os.Stdout)
	}
	f, err := os.Create(out) //nolint:gosec
	if err != nil {
		return err
	}
	defer f.Close()
	if err := session.FormatDiff(s, path, f); err != nil {
		return err
	}
	fmt.Printf("Wrote %s\n", out)
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
