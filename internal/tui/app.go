package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"github.com/bouwerp/ageni/internal/agent"
	"github.com/bouwerp/ageni/internal/llm"
)

// Mode toggles between the chat UI and the settings form.
type Mode int

const (
	ModeChat Mode = iota
	ModeSettings
)

// ReloadFunc rebuilds adapters from the current ~/.ageni/.env and applies
// them to the live master + manager. Returns an error if the new config is
// invalid; callers should preserve the running session in that case.
type ReloadFunc func() error

// CancelFunc cancels any in-flight master generation and all running
// sub-agents. Returns a count of sub-agents cancelled (0 if just master).
type CancelFunc func() int

// App is the top-level Bubble Tea model.
type App struct {
	bus            *agent.Bus
	manager        *agent.Manager
	tracker        *llm.Tracker
	masterIn       chan<- agent.Event
	reload         ReloadFunc
	cancelInFlight CancelFunc

	chat   viewport.Model
	side   viewport.Model
	input  textarea.Model
	usage  string
	width  int
	height int

	// Buffers
	chatBuf       strings.Builder
	currentMaster strings.Builder
	subBufs       map[string]*strings.Builder
	subStatus     map[string]agent.SubagentStatus
	subOrder      []string

	// view state
	focusInput bool
	viewSub    string // selected subagent id, "" = master chat

	mode          Mode
	settingsForm  *huh.Form
	settingsState *settingsState
	flashMessage  string

	ctx    context.Context
	cancel context.CancelFunc
}

func New(ctx context.Context, bus *agent.Bus, manager *agent.Manager, tracker *llm.Tracker, masterIn chan<- agent.Event, reload ReloadFunc, cancelInFlight CancelFunc) *App {
	cctx, cancel := context.WithCancel(ctx)

	ta := textarea.New()
	ta.Placeholder = "Talk to the master. Enter to send, Shift+Enter for newline. Tab cycles panes. Ctrl+C quits."
	ta.Prompt = "❯ "
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(3)
	ta.ShowLineNumbers = false
	ta.Focus()

	chat := viewport.New(80, 20)
	side := viewport.New(30, 20)

	a := &App{
		bus:            bus,
		manager:        manager,
		tracker:        tracker,
		masterIn:       masterIn,
		reload:         reload,
		cancelInFlight: cancelInFlight,
		chat:           chat,
		side:           side,
		input:          ta,
		focusInput:     true,
		subBufs:        make(map[string]*strings.Builder),
		subStatus:      make(map[string]agent.SubagentStatus),
		ctx:            cctx,
		cancel:         cancel,
	}
	a.chatBuf.WriteString(titleStyle.Render("ageni") + " — type a request to begin\n\n")
	a.refreshChat()
	a.refreshSide()
	return a
}

// Tea Msg types
type busEvtMsg agent.Event
type usageMsg llm.TrackerSnapshot

func (a *App) Init() tea.Cmd {
	return tea.Batch(
		a.subscribeBus(),
		a.subscribeUsage(),
		textarea.Blink,
	)
}

func (a *App) subscribeBus() tea.Cmd {
	sub := a.bus.Subscribe(128)
	return func() tea.Msg {
		ev, ok := <-sub
		if !ok {
			return nil
		}
		// Re-arm by sending self a follow-up cmd via a goroutine pump.
		// Bubble Tea's pattern: each Cmd returns one Msg, so we relay ev and
		// the Update handler issues another subscribeOne to read the next.
		return relayMsg{ev: busEvtMsg(ev), sub: sub}
	}
}

type relayMsg struct {
	ev  busEvtMsg
	sub <-chan agent.Event
}

func (a *App) subscribeOne(sub <-chan agent.Event) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-sub
		if !ok {
			return nil
		}
		return relayMsg{ev: busEvtMsg(ev), sub: sub}
	}
}

func (a *App) subscribeUsage() tea.Cmd {
	sub := a.tracker.Subscribe()
	return func() tea.Msg {
		snap, ok := <-sub
		if !ok {
			return nil
		}
		return relayUsageMsg{snap: usageMsg(snap), sub: sub}
	}
}

type relayUsageMsg struct {
	snap usageMsg
	sub  <-chan llm.TrackerSnapshot
}

func (a *App) subscribeUsageOne(sub <-chan llm.TrackerSnapshot) tea.Cmd {
	return func() tea.Msg {
		snap, ok := <-sub
		if !ok {
			return nil
		}
		return relayUsageMsg{snap: usageMsg(snap), sub: sub}
	}
}

func (a *App) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Window sizing applies to both modes.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		a.width = ws.Width
		a.height = ws.Height
		a.layout()
	}

	if a.mode == ModeSettings {
		return a.updateSettings(msg)
	}
	return a.updateChat(msg)
}

func (a *App) updateChat(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch {
		case msg.Type == tea.KeyCtrlC:
			a.cancel()
			return a, tea.Quit
		case msg.Type == tea.KeyTab:
			a.cycleView()
		case msg.Type == tea.KeyEsc:
			a.stopGeneration()
		case msg.String() == "ctrl+,", msg.String() == "ctrl+s":
			return a, a.openSettings()
		}
		if msg.Type == tea.KeyEnter && !msg.Alt && a.focusInput {
			text := strings.TrimSpace(a.input.Value())
			if text != "" {
				a.input.Reset()
				a.chatBuf.WriteString(userStyle.Render("you ❯ ") + text + "\n\n")
				a.currentMaster.Reset()
				a.refreshChat()
				select {
				case a.masterIn <- agent.Event{Kind: agent.EvUserMessage, Text: text}:
				default:
				}
				return a, nil
			}
		}

	case relayMsg:
		a.handleEvent(agent.Event(msg.ev))
		cmds = append(cmds, a.subscribeOne(msg.sub))

	case relayUsageMsg:
		a.usage = renderUsage(llm.TrackerSnapshot(msg.snap))
		cmds = append(cmds, a.subscribeUsageOne(msg.sub))
	}

	if a.focusInput {
		var cmd tea.Cmd
		a.input, cmd = a.input.Update(msg)
		cmds = append(cmds, cmd)
	} else {
		var cmd tea.Cmd
		a.chat, cmd = a.chat.Update(msg)
		cmds = append(cmds, cmd)
	}

	return a, tea.Batch(cmds...)
}

func (a *App) stopGeneration() {
	if a.cancelInFlight == nil {
		return
	}
	subs := a.cancelInFlight()
	if subs > 0 {
		a.flashMessage = fmt.Sprintf("stopped generation (cancelled master + %d sub-agent(s))", subs)
	} else {
		a.flashMessage = "stopped generation"
	}
}

func (a *App) openSettings() tea.Cmd {
	form, st, err := newSettingsForm()
	if err != nil {
		a.flashMessage = "settings: " + err.Error()
		return nil
	}
	a.settingsForm = form
	a.settingsState = st
	a.mode = ModeSettings
	return a.settingsForm.Init()
}

func (a *App) updateSettings(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Allow Ctrl+C to quit and Esc to bail without saving from anywhere.
	if k, ok := msg.(tea.KeyMsg); ok {
		switch k.Type {
		case tea.KeyCtrlC:
			a.cancel()
			return a, tea.Quit
		case tea.KeyEsc:
			a.mode = ModeChat
			a.flashMessage = "settings: cancelled"
			return a, nil
		}
	}

	f, cmd := a.settingsForm.Update(msg)
	if form, ok := f.(*huh.Form); ok {
		a.settingsForm = form
	}

	if a.settingsForm.State == huh.StateCompleted {
		if err := a.settingsState.save(); err != nil {
			a.flashMessage = "settings: save failed — " + err.Error()
		} else if a.reload != nil {
			if err := a.reload(); err != nil {
				a.flashMessage = "settings saved, reload failed: " + err.Error() + " (running adapters unchanged)"
			} else {
				a.flashMessage = "settings applied — master: " + a.settingsState.masterProvider + "/" + a.settingsState.masterModel + ", sub-agent: " + a.settingsState.subProvider + "/" + a.settingsState.subModel
			}
		} else {
			a.flashMessage = "settings saved — restart `ageni` to apply"
		}
		a.mode = ModeChat
		a.refreshChat()
		return a, nil
	}
	return a, cmd
}

func (a *App) cycleView() {
	// "" -> first subagent -> next -> ... -> ""
	if len(a.subOrder) == 0 {
		return
	}
	if a.viewSub == "" {
		a.viewSub = a.subOrder[0]
	} else {
		idx := -1
		for i, id := range a.subOrder {
			if id == a.viewSub {
				idx = i
				break
			}
		}
		if idx < 0 || idx == len(a.subOrder)-1 {
			a.viewSub = ""
		} else {
			a.viewSub = a.subOrder[idx+1]
		}
	}
	a.refreshChat()
}

func (a *App) layout() {
	if a.width < 60 || a.height < 12 {
		return
	}
	sideW := 30
	if a.width < 100 {
		sideW = 24
	}
	chatW := a.width - sideW - 4
	inputH := 4
	statusH := 1
	bodyH := a.height - inputH - statusH - 2

	a.chat.Width = chatW
	a.chat.Height = bodyH
	a.side.Width = sideW
	a.side.Height = bodyH
	a.input.SetWidth(a.width - 4)
	a.input.SetHeight(inputH - 2)
	a.refreshChat()
	a.refreshSide()
}

func (a *App) handleEvent(ev agent.Event) {
	switch ev.Kind {
	case agent.EvMasterText:
		a.currentMaster.WriteString(ev.Text)
		a.appendMasterRender()
	case agent.EvMasterToolCall:
		if ev.ToolCall != nil {
			a.chatBuf.WriteString(mutedStyle.Render(fmt.Sprintf("• tool: %s(%s)\n", ev.ToolCall.Name, compactArgs(ev.ToolCall.Arguments))))
			a.refreshChat()
		}
	case agent.EvMasterToolDone:
		if ev.ToolResult != nil {
			snip := ev.ToolResult.Content
			if len(snip) > 200 {
				snip = snip[:200] + "…"
			}
			mark := ""
			if ev.ToolResult.IsError {
				mark = " [ERROR]"
			}
			a.chatBuf.WriteString(mutedStyle.Render(fmt.Sprintf("  ↳%s %s\n", mark, oneLine(snip))))
			a.refreshChat()
		}
	case agent.EvMasterTurnDone:
		a.flushMasterText()
		a.refreshChat()
	case agent.EvSubagentSpawn:
		a.subBufs[ev.SubagentID] = &strings.Builder{}
		a.subStatus[ev.SubagentID] = agent.StatusRunning
		a.subOrder = append(a.subOrder, ev.SubagentID)
		a.subBufs[ev.SubagentID].WriteString(fmt.Sprintf("# %s\n%s\n\n", ev.SubagentID, ev.SubagentTask))
		a.refreshSide()
		a.refreshChat()
	case agent.EvSubagentText:
		if b, ok := a.subBufs[ev.SubagentID]; ok {
			b.WriteString(ev.Text)
		}
		a.refreshChat()
	case agent.EvSubagentToolCall:
		if b, ok := a.subBufs[ev.SubagentID]; ok && ev.ToolCall != nil {
			b.WriteString(fmt.Sprintf("\n• tool: %s\n", ev.ToolCall.Name))
		}
		a.refreshChat()
	case agent.EvSubagentToolDone:
		// noop here; transcript already shows tool flow
	case agent.EvSubagentDone:
		a.subStatus[ev.SubagentID] = agent.StatusDone
		a.refreshSide()
	case agent.EvSubagentError:
		a.subStatus[ev.SubagentID] = agent.StatusError
		a.refreshSide()
	case agent.EvError:
		a.chatBuf.WriteString(subErrStyle.Render(fmt.Sprintf("\n[error] %v\n", ev.Err)))
		a.refreshChat()
	}
}

func (a *App) appendMasterRender() {
	// Live-render the in-progress master text as the trailing block.
	a.refreshChat()
}

func (a *App) flushMasterText() {
	if a.currentMaster.Len() > 0 {
		a.chatBuf.WriteString(titleStyle.Render("master ❯ ") + a.currentMaster.String() + "\n\n")
		a.currentMaster.Reset()
	}
}

func (a *App) refreshChat() {
	if a.viewSub != "" {
		if b, ok := a.subBufs[a.viewSub]; ok {
			a.chat.SetContent(b.String())
			a.chat.GotoBottom()
			return
		}
	}
	body := a.chatBuf.String()
	if a.currentMaster.Len() > 0 {
		body += titleStyle.Render("master ❯ ") + a.currentMaster.String()
	}
	a.chat.SetContent(body)
	a.chat.GotoBottom()
}

func (a *App) refreshSide() {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("sub-agents") + "\n\n")
	if len(a.subOrder) == 0 {
		sb.WriteString(mutedStyle.Render("(none yet)\n"))
	}
	for _, id := range a.subOrder {
		st := a.subStatus[id]
		marker := "•"
		st2 := subRunningStyle
		switch st {
		case agent.StatusDone:
			st2 = subDoneStyle
			marker = "✓"
		case agent.StatusError, agent.StatusCancelled:
			st2 = subErrStyle
			marker = "✗"
		}
		line := fmt.Sprintf("%s %s  %s", marker, id, st)
		if id == a.viewSub {
			line = lipgloss.NewStyle().Reverse(true).Render(line)
		}
		sb.WriteString(st2.Render(line) + "\n")
	}
	a.side.SetContent(sb.String())
}

func (a *App) View() string {
	if a.width < 60 || a.height < 12 {
		return "ageni: window too small (need 60×12)"
	}
	if a.mode == ModeSettings && a.settingsForm != nil {
		header := titleStyle.Render("Settings") + statusStyle.Render("  Esc=cancel without saving  Enter=advance/submit\n\n")
		return header + a.settingsForm.View()
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		chatStyle.Render(a.chat.View()),
		sideStyle.Render(a.side.View()),
	)
	in := inputStyle.Render(a.input.View())
	bottom := statusStyle.Render(a.statusLine())
	return lipgloss.JoinVertical(lipgloss.Left, body, in, bottom)
}

func (a *App) statusLine() string {
	view := "view: master"
	if a.viewSub != "" {
		view = "view: " + a.viewSub
	}
	flash := ""
	if a.flashMessage != "" {
		flash = "  │  " + a.flashMessage
	}
	return fmt.Sprintf("%s  │  %s  │  Tab=cycle  Esc=stop  Ctrl+,=settings  Ctrl+C=quit%s", view, a.usage, flash)
}

func renderUsage(snap llm.TrackerSnapshot) string {
	t := snap.Total
	cacheRate := 0.0
	denom := t.InputTokens + t.CacheReadTokens
	if denom > 0 {
		cacheRate = 100 * float64(t.CacheReadTokens) / float64(denom)
	}
	return fmt.Sprintf("tokens in=%d out=%d cache=%d (%.0f%% hit) created=%d",
		t.InputTokens, t.OutputTokens, t.CacheReadTokens, cacheRate, t.CacheCreationTokens)
}

func compactArgs(b []byte) string {
	s := string(b)
	if len(s) > 80 {
		return s[:80] + "…"
	}
	return s
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
