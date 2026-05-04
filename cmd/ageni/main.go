package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/bouwerp/ageni/internal/agent"
	"github.com/bouwerp/ageni/internal/config"
	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/session"
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
	fmt.Fprintln(w, "  update           update ageni to the latest release")
	fmt.Fprintln(w, "  help, -h         show this help")
}

func run() error {
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
	registerBase := func(r *tools.Registry, todo *tools.TodoWrite) {
		r.Register(tools.ReadFile{})
		r.Register(tools.WriteFile{})
		r.Register(tools.EditFile{})
		r.Register(tools.MultiEdit{})
		r.Register(tools.ListDir{})
		r.Register(tools.Glob{})
		r.Register(tools.Grep{})
		r.Register(tools.RunBash{})
		r.Register(tools.WebFetch{})
		r.Register(tools.WebSearch{})
		r.Register(todo)
	}

	// One TodoWrite instance shared between master and sub-agents so the
	// session todo list is a single source of truth.
	todo := tools.NewTodoWrite()

	registry := tools.NewRegistry()
	registerBase(registry, todo)

	// Manager + master-only tools
	manager := agent.NewManager(bus, registry, tracker, factory, cfg.MaxSubagents)

	masterReg := tools.NewRegistry()
	registerBase(masterReg, todo)
	masterReg.Register(agent.SpawnTool{M: manager})
	masterReg.Register(agent.CheckTool{M: manager})
	masterReg.Register(agent.SendTool{M: manager})
	masterReg.Register(agent.KillTool{M: manager})

	// Master loop
	master := agent.NewMaster(masterAdapter, cfg.Master.Model, masterReg, bus, tracker, manager)
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
	logger, err := session.NewLogger()
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
		return nil
	}

	cancelInFlight := func() int {
		master.CancelCurrent()
		return manager.CancelAll()
	}

	// TUI
	app := tui.New(ctx, bus, manager, tracker, masterIn, reload, cancelInFlight)
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
		"AGENI_MAX_SUBAGENTS",
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
