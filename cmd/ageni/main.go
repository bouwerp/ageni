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
	registry := tools.NewRegistry()

	// Built-in tools (master + sub-agents both get these)
	registry.Register(tools.ReadFile{})
	registry.Register(tools.WriteFile{})
	registry.Register(tools.EditFile{})
	registry.Register(tools.ListDir{})
	registry.Register(tools.RunBash{})

	// Manager + master-only tools
	manager := agent.NewManager(bus, registry, tracker, factory, cfg.MaxSubagents)

	// Master gets a separate registry that includes the spawn/check/send/kill tools.
	masterReg := tools.NewRegistry()
	masterReg.Register(tools.ReadFile{})
	masterReg.Register(tools.WriteFile{})
	masterReg.Register(tools.EditFile{})
	masterReg.Register(tools.ListDir{})
	masterReg.Register(tools.RunBash{})
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

	// TUI
	app := tui.New(ctx, bus, manager, tracker, masterIn)
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

func handleSignals(cancel context.CancelFunc) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	<-c
	cancel()
}
