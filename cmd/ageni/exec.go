package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/awnumar/memguard"

	"github.com/bouwerp/ageni/internal/agent"
	"github.com/bouwerp/ageni/internal/agentsmd"
	"github.com/bouwerp/ageni/internal/config"
	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/models"
	"github.com/bouwerp/ageni/internal/repomap"
	"github.com/bouwerp/ageni/internal/secrets"
	"github.com/bouwerp/ageni/internal/session"
	"github.com/bouwerp/ageni/internal/tools"
)

func runExec(args []string) error {
	prompt, err := parseExecPrompt(args, os.Stdin, stdinIsTTY(os.Stdin))
	if err != nil {
		return err
	}

	memguard.CatchInterrupt()
	defer memguard.Purge()

	secretStore, storeErr := secrets.OpenEnvOnly()
	if storeErr != nil {
		secretStore, _ = secrets.OpenEnvOnly()
	}

	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotConfigured) {
			return fmt.Errorf("no ageni config found — run `ageni init` first")
		}
		return err
	}
	if secretStore != nil {
		secretStore.SeedFromEnv()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go handleSignals(cancel)

	bus := agent.NewBus()
	tracker := llm.NewTracker()
	onFallback := func(role string) func(from, to, reason string) {
		return func(from, to, reason string) {
			bus.Publish(agent.Event{
				Kind: agent.EvFlash,
				Text: fmt.Sprintf("%s fallback %s → %s (%s)", role, from, to, reason),
			})
		}
	}
	masterAdapter := buildChain("master", cfg.Master, cfg.MasterFallbacks, onFallback("master"))
	subAdapter := buildChain("subagent", cfg.Subagent, cfg.SubagentFallbacks, onFallback("subagent"))
	var masterLeadAdapter llm.Adapter
	if cfg.MasterLeadActive {
		masterLeadAdapter = buildAdapter(cfg.MasterLead)
	}
	var criticAdapter llm.Adapter
	if cfg.CriticActive {
		criticAdapter = buildAdapter(cfg.Critic)
	}
	var compactAdapter llm.Adapter
	if cfg.CompactActive {
		compactAdapter = buildAdapter(cfg.Compact)
	}
	fleet := buildFleet(cfg.LocalFleet)
	subPool := buildCloudSubPool(cfg.SubagentPool)
	factory := buildFactory(cfg, masterAdapter, subAdapter, fleet, subPool)

	mcpMgr, mcpTools := loadMCPTools(ctx)
	if mcpMgr != nil {
		defer mcpMgr.Close()
	}
	skillReg := loadSkillRegistry()
	roleReg := loadRoleRegistry()
	memReg := loadMemoryRegistry()

	sess, err := session.New(detectRepoRoot())
	if err != nil {
		return fmt.Errorf("session init: %w", err)
	}
	sess.SetModels(cfg.Master.Provider.Name, cfg.Master.Model, cfg.Subagent.Provider.Name, cfg.Subagent.Model)

	todo := tools.NewTodoWrite(sess.Path("todo.json"))
	changes := tools.NewChangeTracker(sess.Path("changes.jsonl"), sess.Path("snapshots"))

	registry := tools.NewRegistry()
	registerWorkerBase(registry, todo, changes, skillReg, memReg, mcpTools, secretStore)
	{
		var visionTool tools.ViewImage
		if cfg.VisionActive {
			visionTool = tools.ViewImage{
				APIKey:         cfg.Vision.APIKey,
				BaseURL:        cfg.Vision.BaseURL,
				Model:          cfg.Vision.Model,
				SupportsVision: true,
			}
		} else {
			visionModel := cfg.Master.Model
			if vm := os.Getenv("VISION_MODEL"); vm != "" {
				visionModel = vm
			}
			visionTool = tools.ViewImage{
				APIKey:  cfg.Master.APIKey,
				BaseURL: cfg.Master.BaseURL,
				Model:   visionModel,
			}
		}
		registry.Register(visionTool)
	}

	manager := agent.NewManager(ctx, bus, registry, tracker, factory, cfg.MaxSubagents)
	manager.SetDefaultBudget(cfg.SubagentBudget)
	if secretStore != nil {
		manager.SetScrubber(secretStore.Redactor().Scrub)
	}
	shellMgr := agent.NewShellManager(bus)
	defer shellMgr.CancelAll()

	registry.Register(agent.FindInCodebase{M: manager, Bus: bus})
	registry.Register(agent.OpenShellTool{SM: shellMgr})
	registry.Register(agent.ShellExecTool{SM: shellMgr})
	registry.Register(agent.ShellReadTool{SM: shellMgr})
	registry.Register(agent.ShellWaitTool{SM: shellMgr})
	registry.Register(agent.ShellSendInputTool{SM: shellMgr})
	registry.Register(agent.InterruptShellTool{SM: shellMgr})
	registry.Register(agent.CloseShellTool{SM: shellMgr})
	registry.Register(agent.ListShellsTool{SM: shellMgr})

	masterReg := tools.NewRegistry()
	registerMasterBase(masterReg, todo, skillReg, memReg, secretStore)
	corrections := tools.NewRecordCorrection(sess.Path("corrections.jsonl"))
	masterReg.Register(corrections)
	masterReg.Register(agent.SpawnTool{M: manager})
	masterReg.Register(agent.CheckTool{M: manager})
	masterReg.Register(agent.SendTool{M: manager})
	masterReg.Register(agent.KillTool{M: manager})
	masterReg.Register(agent.PauseTool{M: manager})
	masterReg.Register(agent.ResumeTool{M: manager})
	masterReg.Register(agent.FindInCodebase{M: manager, Bus: bus})
	masterReg.Register(agent.OpenShellTool{SM: shellMgr})
	masterReg.Register(agent.ShellExecTool{SM: shellMgr})
	masterReg.Register(agent.ShellReadTool{SM: shellMgr})
	masterReg.Register(agent.ShellWaitTool{SM: shellMgr})
	masterReg.Register(agent.ShellSendInputTool{SM: shellMgr})
	masterReg.Register(agent.InterruptShellTool{SM: shellMgr})
	masterReg.Register(agent.CloseShellTool{SM: shellMgr})
	masterReg.Register(agent.ListShellsTool{SM: shellMgr})

	master := agent.NewMaster(masterAdapter, cfg.Master.Model, masterReg, bus, tracker, manager)
	master.SetTodo(todo)
	{
		masterCaps := models.Global.CapabilitiesForModel(cfg.Master.Model)
		subCapsSet := map[string]struct{}{}
		if cfg.VisionActive {
			subCapsSet["vision"] = struct{}{}
		}
		for _, rc := range cfg.SubagentPool {
			for _, c := range models.Global.CapabilitiesForModel(rc.Model) {
				subCapsSet[c] = struct{}{}
			}
		}
		if subCaps := models.Global.CapabilitiesForModel(cfg.Subagent.Model); len(cfg.SubagentPool) == 0 {
			for _, c := range subCaps {
				subCapsSet[c] = struct{}{}
			}
		}
		subagentCaps := make([]string, 0, len(subCapsSet))
		for c := range subCapsSet {
			subagentCaps = append(subagentCaps, c)
		}
		master.SetCapabilities(masterCaps, subagentCaps)
	}
	if secretStore != nil {
		master.SetScrubber(secretStore.Redactor().Scrub)
	}
	if masterLeadAdapter != nil {
		master.SetLead(masterLeadAdapter, cfg.MasterLead.Model)
	}
	if criticAdapter != nil {
		master.SetCritic(criticAdapter, cfg.Critic.Model)
	}
	if compactAdapter != nil {
		master.SetCompact(compactAdapter, cfg.Compact.Model)
	}
	master.SetCorrectionsPath(sess.Path("corrections.jsonl"))
	if skillReg != nil {
		catalog := skillReg.Catalog()
		master.SetSkillCatalog(catalog)
		manager.SetSkillCatalog(catalog)
	}
	if roleReg != nil {
		roleCatalog := roleReg.Catalog()
		master.SetRoleCatalog(roleCatalog)
		manager.SetRoleCatalog(roleCatalog)
	}
	if memReg != nil {
		master.SetMemoryRegistry(memReg)
		manager.SetMemoryRegistry(memReg)
	}
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
	if root := detectRepoRoot(); root != "" {
		if res, err := agentsmd.Load(root); err == nil && res.Rendered != "" {
			master.SetAgentsMD(res.Rendered)
		}
	}

	masterIn := make(chan agent.Event, 256)
	subFwd := bus.Subscribe(128)
	go func() {
		for ev := range subFwd {
			switch ev.Kind {
			case agent.EvSubagentSpawn,
				agent.EvSubagentToolCall,
				agent.EvSubagentToolDone,
				agent.EvSubagentRetry,
				agent.EvSubagentInbox,
				agent.EvSubagentUsage,
				agent.EvSubagentDone,
				agent.EvSubagentError,
				agent.EvShellOpened,
				agent.EvShellExited:
				select {
				case masterIn <- ev:
				default:
				}
			}
		}
	}()
	go master.Run(ctx, masterIn)

	logger, err := session.NewLogger(sess)
	if err != nil {
		return fmt.Errorf("session log: %w", err)
	}
	defer logger.Close()
	bus.AddSink(logger)

	resultSub := bus.Subscribe(256)
	select {
	case masterIn <- agent.Event{Kind: agent.EvUserMessage, Text: prompt}:
	case <-ctx.Done():
		return ctx.Err()
	}
	result, err := waitForHeadlessResult(ctx, resultSub)
	if err != nil {
		return err
	}
	if result != "" {
		fmt.Print(result)
		if !strings.HasSuffix(result, "\n") {
			fmt.Println()
		}
	}
	return nil
}

func parseExecPrompt(args []string, stdin io.Reader, stdinIsTTY bool) (string, error) {
	if len(args) > 0 {
		prompt := strings.TrimSpace(strings.Join(args, " "))
		if prompt != "" {
			return prompt, nil
		}
	}
	if !stdinIsTTY {
		b, err := io.ReadAll(stdin)
		if err != nil {
			return "", err
		}
		if prompt := strings.TrimSpace(string(b)); prompt != "" {
			return prompt, nil
		}
	}
	return "", fmt.Errorf("usage: ageni exec <prompt>  (or pipe prompt on stdin)")
}

func stdinIsTTY(f *os.File) bool {
	if f == nil {
		return true
	}
	info, err := f.Stat()
	if err != nil {
		return true
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func waitForHeadlessResult(ctx context.Context, events <-chan agent.Event) (string, error) {
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case ev, ok := <-events:
			if !ok {
				return "", errors.New("exec event stream closed before completion")
			}
			switch ev.Kind {
			case agent.EvMasterTurnDone:
				return ev.Text, nil
			case agent.EvError:
				if ev.Err != nil {
					return "", ev.Err
				}
				return "", errors.New("headless exec failed")
			}
		}
	}
}
