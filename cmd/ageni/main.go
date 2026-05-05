package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/bouwerp/ageni/internal/agent"
	"github.com/bouwerp/ageni/internal/config"
	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/mcp"
	"github.com/bouwerp/ageni/internal/repomap"
	"github.com/bouwerp/ageni/internal/session"
	"github.com/bouwerp/ageni/internal/skills"
	"github.com/bouwerp/ageni/internal/tools"
	"github.com/bouwerp/ageni/internal/tui"
)

// Set via -ldflags at build time. See Makefile / release.yml.
var (
	version   = "dev"
	buildTime = ""
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version", "-v", "version":
			printVersion()
			return
		case "update":
			if err := runUpdate(version); err != nil {
				fmt.Fprintln(os.Stderr, "ageni: "+err.Error())
				os.Exit(1)
			}
			return
		case "init":
			if err := runInit(); err != nil {
				fmt.Fprintln(os.Stderr, "ageni: "+err.Error())
				os.Exit(1)
			}
			return
		case "skills":
			if err := runSkills(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "ageni: "+err.Error())
				os.Exit(1)
			}
			return
		case "sessions":
			if err := runSessions(os.Args[2:]); err != nil {
				fmt.Fprintln(os.Stderr, "ageni: "+err.Error())
				os.Exit(1)
			}
			return
		case "doctor":
			autoInstall := false
			for _, a := range os.Args[2:] {
				if a == "--install" || a == "-y" {
					autoInstall = true
				}
			}
			if err := runDoctor(autoInstall); err != nil {
				fmt.Fprintln(os.Stderr, "ageni: "+err.Error())
				os.Exit(1)
			}
			return
		case "--help", "-h", "help":
			printUsage(os.Stdout)
			return
		default:
			fmt.Fprintf(os.Stderr, "ageni: unknown command %q\n\n", os.Args[1])
			printUsage(os.Stderr)
			os.Exit(1)
		}
	}

	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ageni: "+err.Error())
		os.Exit(1)
	}
}

func printVersion() {
	if buildTime != "" {
		fmt.Printf("ageni %s (built %s)\n", version, buildTime)
	} else {
		fmt.Printf("ageni %s\n", version)
	}
}

func printUsage(w *os.File) {
	fmt.Fprintln(w, "Usage: ageni [command]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  (none)           start the TUI")
	fmt.Fprintln(w, "  version, -v      print version information")
	fmt.Fprintln(w, "  init             interactive config wizard")
	fmt.Fprintln(w, "  doctor           check external CLI dependencies; --install / -y to auto-install")
	fmt.Fprintln(w, "  skills <cmd>     manage skills (list, install <git-url>, path)")
	fmt.Fprintln(w, "  sessions <cmd>   manage sessions (list, show, resume, rm)")
	fmt.Fprintln(w, "  update           update ageni to the latest release")
	fmt.Fprintln(w, "  help, -h         show this help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Flags (when starting the TUI):")
	fmt.Fprintln(w, "  --session <id>   resume an existing session instead of starting a new one")
}

func run() error {
	// Force lipgloss + termenv to TrueColor before Bubble Tea starts. Auto-
	// detection inside the alt-screen sometimes lands on the Ascii profile
	// (most commonly when stdout's TTY query fails), which strips every
	// styled escape and leaves users with unstyled markdown and plain
	// borders. Modern terminals all accept TrueColor escapes; older ones
	// degrade gracefully.
	lipgloss.SetColorProfile(termenv.TrueColor)

	cfg, err := config.Load()
	if err != nil {
		// First-run UX: drop into the wizard if no provider is configured.
		if errors.Is(err, config.ErrNotConfigured) {
			fmt.Println("No ageni config found — running first-time setup.")
			fmt.Println()
			if werr := runInit(); werr != nil {
				return werr
			}
			cfg, err = config.Load()
			if err != nil {
				return err
			}
		} else {
			return err
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handleSignals(cancel)

	masterAdapter := buildAdapter(cfg.Master)
	subAdapter := buildAdapter(cfg.Subagent)

	// Tier factory. v1: opus → master adapter, others → sub-agent adapter.
	// (Per-tier model overrides land in v2.)
	factory := func(tier string) (llm.Adapter, string) {
		switch tier {
		case "opus":
			return masterAdapter, cfg.Master.Model
		default:
			return subAdapter, cfg.Subagent.Model
		}
	}

	bus := agent.NewBus()
	tracker := llm.NewTracker()

	// Build a base set of tools used by both master and sub-agents.
	// Connect to any configured MCP servers (~/.ageni/mcp.json) and collect
	// their tools to add to the registries below.
	mcpMgr, mcpTools, mcpErr := mcp.LoadAndConnect(ctx)
	if mcpErr != nil {
		fmt.Fprintf(os.Stderr, "ageni: mcp setup: %v\n", mcpErr)
	}
	if mcpMgr != nil {
		defer mcpMgr.Close()
	}

	// Load skills from ~/.ageni/skills/ and ./.ageni/skills/.
	skillReg, sErr := skills.Load()
	if sErr != nil {
		fmt.Fprintf(os.Stderr, "ageni: skills: %v\n", sErr)
		skillReg = nil
	}

	registerBase := func(r *tools.Registry, todo *tools.TodoWrite, tr *tools.ChangeTracker) {
		r.Register(tools.ReadFile{})
		r.Register(tools.WriteFile{Tracker: tr})
		r.Register(tools.EditFile{Tracker: tr})
		r.Register(tools.MultiEdit{Tracker: tr})
		r.Register(tools.ListDir{})
		r.Register(tools.Glob{})
		r.Register(tools.Grep{})
		r.Register(tools.MakeDir{Tracker: tr})
		r.Register(tools.MoveFile{Tracker: tr})
		r.Register(tools.DeleteFile{Tracker: tr})
		r.Register(tools.RunBash{})
		r.Register(tools.WebFetch{})
		r.Register(tools.WebSearch{})
		r.Register(tools.GitStatus{})
		r.Register(tools.GitDiff{})
		r.Register(tools.GitLog{})
		r.Register(tools.ComputeDiff{})
		r.Register(tools.RunTests{})
		r.Register(tools.GitHub{})
		r.Register(tools.PkgInfo{})
		r.Register(todo)
		if skillReg != nil {
			r.Register(skills.ReadSkill{Registry: skillReg})
		}
		for _, t := range mcpTools {
			r.Register(t)
		}
	}

	// Open a session — either resume an existing one (--session <id>) or
	// start fresh. Per-instance state lives under ~/.ageni/sessions/<id>/
	// so multiple instances in the same repo never collide.
	sess, sessErr := openOrCreateSession()
	if sessErr != nil {
		return fmt.Errorf("session init: %w", sessErr)
	}
	sess.SetModels(cfg.Master.Provider.Name, cfg.Master.Model,
		cfg.Subagent.Provider.Name, cfg.Subagent.Model)

	// One TodoWrite instance shared between master and sub-agents so the
	// session todo list is a single source of truth — now scoped to the
	// session dir.
	todo := tools.NewTodoWrite(sess.Path("todo.json"))

	// One ChangeTracker shared between master and sub-agents. Records
	// every file mutation with a pre-mutation snapshot under
	// <session_dir>/snapshots so we can produce real diffs later via
	// `ageni sessions diff`.
	changes := tools.NewChangeTracker(sess.Path("changes.jsonl"), sess.Path("snapshots"))

	registry := tools.NewRegistry()
	registerBase(registry, todo, changes)

	// Manager + master-only tools. Pass the app-wide ctx so sub-agents
	// inherit a lifetime that outlives any individual master turn.
	manager := agent.NewManager(ctx, bus, registry, tracker, factory, cfg.MaxSubagents)
	manager.SetDefaultBudget(cfg.SubagentBudget)

	// find_in_codebase is available to sub-agents as well as the master.
	// The master's prompt promotes it heavily, that vocabulary leaks into
	// spawn_subagent contexts, and workers were hallucinating the call —
	// producing "unknown tool find_in_codebase". Recursion is bounded by
	// each worker's tool-call budget and the manager's max-concurrent cap;
	// the Librarian sub-agent itself can't recurse because its own
	// AllowedTools whitelist (in find_tool.go) excludes find_in_codebase.
	registry.Register(agent.FindInCodebase{M: manager, Bus: bus})

	masterReg := tools.NewRegistry()
	registerBase(masterReg, todo, changes)
	corrections := tools.NewRecordCorrection(sess.Path("corrections.jsonl"))
	masterReg.Register(corrections)
	masterReg.Register(agent.SpawnTool{M: manager})
	masterReg.Register(agent.CheckTool{M: manager})
	masterReg.Register(agent.SendTool{M: manager})
	masterReg.Register(agent.KillTool{M: manager})
	masterReg.Register(agent.FindInCodebase{M: manager, Bus: bus})

	// Master loop
	master := agent.NewMaster(masterAdapter, cfg.Master.Model, masterReg, bus, tracker, manager)
	master.SetCorrectionsPath(sess.Path("corrections.jsonl"))
	if skillReg != nil {
		catalog := skillReg.Catalog()
		master.SetSkillCatalog(catalog)
		manager.SetSkillCatalog(catalog)
	}

	// Build the Aider-style repo map asynchronously so the TUI starts
	// instantly. The map gets installed as soon as it's ready; until then
	// the master runs without it.
	go func() {
		root := detectRepoRoot()
		if root == "" {
			return
		}
		m, err := repomap.LoadOrBuild(ctx, root, repomap.Options{MaxTokens: 2000})
		if err != nil || m == nil || m.Rendered == "" {
			return
		}
		master.SetRepoMap(m.Rendered)
	}()
	masterIn := make(chan agent.Event, 16)

	// Forward sub-agent events from the bus into the master inbox so it can react.
	subFwd := bus.Subscribe(128)
	go func() {
		for ev := range subFwd {
			if ev.Kind == agent.EvSubagentDone || ev.Kind == agent.EvSubagentError {
				select {
				case masterIn <- ev:
				default:
				}
			}
		}
	}()

	go master.Run(ctx, masterIn)

	// Session log
	logger, err := session.NewLogger(sess)
	if err != nil {
		return fmt.Errorf("session log: %w", err)
	}
	defer logger.Close()
	logSub := bus.Subscribe(256)
	go logger.Run(ctx, logSub)

	// Hot-reload callback: re-reads ~/.ageni/.env, rebuilds adapters, swaps
	// them into master + manager. Triggered by the in-TUI settings page.
	reload := func() error {
		// Clear cached values so godotenv picks up edits.
		clearAgeniEnv()
		newCfg, err := config.Load()
		if err != nil {
			return err
		}
		newMasterAdapter := buildAdapter(newCfg.Master)
		newSubAdapter := buildAdapter(newCfg.Subagent)
		newFactory := func(tier string) (llm.Adapter, string) {
			switch tier {
			case "opus":
				return newMasterAdapter, newCfg.Master.Model
			default:
				return newSubAdapter, newCfg.Subagent.Model
			}
		}
		master.UpdateAdapter(newMasterAdapter, newCfg.Master.Model)
		manager.UpdateFactory(newFactory)
		manager.SetDefaultBudget(newCfg.SubagentBudget)
		return nil
	}

	cancelInFlight := func() int {
		master.CancelCurrent()
		return manager.CancelAll()
	}

	// TUI
	app := tui.New(ctx, bus, manager, tracker, masterIn, reload, cancelInFlight, sess, todo, changes)
	prog := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := prog.Run(); err != nil {
		return err
	}
	return nil
}

// buildAdapter returns the right Adapter for a resolved RoleConfig.
func buildAdapter(rc config.RoleConfig) llm.Adapter {
	switch rc.Provider.Kind {
	case llm.KindAnthropic:
		return llm.NewAnthropicAdapter(rc.APIKey)
	default:
		return llm.NewOpenAIAdapter(rc.APIKey, rc.BaseURL)
	}
}

// openOrCreateSession parses os.Args for "--session <id>" / "--session=<id>"
// and resumes that session if found; otherwise creates a fresh one. The
// flag is removed from os.Args so other parsers don't see it.
func openOrCreateSession() (*session.Session, error) {
	args := os.Args[1:]
	var resumeID string
	cleaned := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--session":
			if i+1 < len(args) {
				resumeID = args[i+1]
				i++
			}
		case strings.HasPrefix(args[i], "--session="):
			resumeID = strings.TrimPrefix(args[i], "--session=")
		default:
			cleaned = append(cleaned, args[i])
		}
	}
	os.Args = append(os.Args[:1], cleaned...)

	if resumeID == "" {
		return session.New(detectRepoRoot())
	}
	id, err := session.ResolveID(resumeID)
	if err != nil {
		return nil, err
	}
	s, err := session.Open(id)
	if err != nil {
		return nil, err
	}
	fmt.Printf("Resuming session %s (last used %s)\n", s.ID, humaniseTime(s.LastUsed))
	return s, nil
}

func humaniseTime(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02 15:04")
	}
}

// detectRepoRoot returns the absolute path to the current git repo's root,
// or the cwd if not inside a git repo. Empty string on error.
func detectRepoRoot() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err == nil {
		root := strings.TrimSpace(string(out))
		if root != "" {
			return root
		}
	}
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return cwd
}

// clearAgeniEnv unsets all ageni-managed env vars so a subsequent
// config.Load() re-reads them from the (possibly updated) ~/.ageni/.env or
// ./.env file. Without this, godotenv would skip vars already set in
// os.Environ from the previous load and the hot-reload would be a no-op.
func clearAgeniEnv() {
	keys := []string{
		"MASTER_PROVIDER", "MASTER_MODEL", "MASTER_BASE_URL", "MASTER_API_KEY",
		"SUBAGENT_PROVIDER", "SUBAGENT_MODEL", "SUBAGENT_BASE_URL", "SUBAGENT_API_KEY",
		"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "GROQ_API_KEY",
		"HF_TOKEN", "CEREBRAS_API_KEY", "MISTRAL_API_KEY", "DEEPSEEK_API_KEY",
		"GEMINI_API_KEY", "OLLAMA_API_KEY", "OPENAI_BASE_URL",
		"AGENI_MAX_SUBAGENTS", "AGENI_SUBAGENT_BUDGET",
	}
	for _, k := range keys {
		_ = os.Unsetenv(k)
	}
}

func handleSignals(cancel context.CancelFunc) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	<-c
	cancel()
}
