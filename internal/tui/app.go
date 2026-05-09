package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/bouwerp/ageni/internal/agent"
	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/session"
	"github.com/bouwerp/ageni/internal/tools"
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
	session        *session.Session
	todo           *tools.TodoWrite
	changes        *tools.ChangeTracker

	chat   viewport.Model
	side   viewport.Model
	input  textarea.Model
	usage  string
	width  int
	height int

	// Buffers
	chatBuf           strings.Builder
	currentMaster     strings.Builder
	currentReasoning  strings.Builder // in-progress reasoning/thinking delta
	currentSubText    map[string]*strings.Builder // in-progress text per sub-agent
	subBufs           map[string]*strings.Builder
	subStatus      map[string]agent.SubagentStatus
	subOrder       []string

	// view state
	focusInput bool
	viewSub    string // selected subagent id, "" = master chat

	mode Mode

	// Settings — the settings screen has two phases:
	//   Phase 0: providerList (custom component, inline checkbox + key inputs)
	//   Phase 1: settingsForm (huh form for roles / fallbacks / limits)
	settingsPhase int
	providerList  *providerListModel
	settingsForm  *huh.Form
	settingsState *settingsState
	flashMessage  string

	// mouseOn tracks whether Bubble Tea's mouse capture is enabled.
	// Toggled with F2 so the user can drag-select text in the terminal.
	mouseOn bool

	// atComp is non-nil while @ path autocomplete is active.
	atComp *atComplete

	// pendingCalls maps ToolCall.ID → ToolCall so that EvMasterToolDone /
	// EvSubagentToolDone handlers can look up which file was mutated and
	// show a diff.
	pendingCalls map[string]llm.ToolCall

	// Markdown renderer for master + sub-agent final output. Re-initialised
	// when the chat-pane width changes.
	glam      *glamour.TermRenderer
	glamWidth int

	// Command history with up/down arrow + persistence.
	history      *History
	historyIdx   int    // -1 = not browsing, otherwise index into history items
	historyDraft string // input text saved when the user enters history mode

	// needsReorientation is set by LoadHistory when a prior session is
	// resumed. Init fires a reorientMsg so the master summarises where
	// the session left off before waiting for the user's first message.
	needsReorientation bool

	// Activity indicator state.
	//
	// masterBusy is true while master generation is in flight: set on
	// EvMasterTurnStart (the moment the LLM call goes out) and cleared on
	// EvMasterTurnDone. Driving it from the bus event — rather than the
	// user's Enter press — means the indicator also lights up when a
	// sub-agent completion triggers a follow-up master turn.
	//
	// masterToolIn is the name of the master tool currently executing.
	// subActivity[id] is one of "" / "thinking" / "tool:NAME" — what each
	// running sub-agent is doing right now, used by the side-pane label.
	// spinFrame advances on every tickMsg.
	masterBusy   bool
	spinFrame    int
	masterToolIn string
	subActivity  map[string]string

	// msgQueue holds user messages submitted while the master is busy.
	// They are dequeued one at a time when the master becomes idle.
	// Esc (stopGeneration) clears the queue.
	msgQueue []string

	// masterModel is the "provider/model" label shown in the status bar.
	// Populated at startup from env and refreshed after settings reload.
	masterModel string

	ctx    context.Context
	cancel context.CancelFunc
}

func New(ctx context.Context, bus *agent.Bus, manager *agent.Manager, tracker *llm.Tracker, masterIn chan<- agent.Event, reload ReloadFunc, cancelInFlight CancelFunc, sess *session.Session, todo *tools.TodoWrite, changes *tools.ChangeTracker) *App {
	cctx, cancel := context.WithCancel(ctx)

	ta := textarea.New()
	ta.Placeholder = "Talk to the master. @path attaches a file. Enter to send, Shift+Enter for newline."
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
		session:        sess,
		todo:           todo,
		changes:        changes,
		chat:           chat,
		side:           side,
		input:          ta,
		focusInput:     true,
		subBufs:        make(map[string]*strings.Builder),
		currentSubText: make(map[string]*strings.Builder),
		subStatus:      make(map[string]agent.SubagentStatus),
		subActivity:    make(map[string]string),
		history:      LoadHistory(),
		historyIdx:   -1,
		mouseOn:      true,
		ctx:          cctx,
		cancel:       cancel,
		pendingCalls: make(map[string]llm.ToolCall),
	}
	// Show the active master model in the status bar. The env vars are already
	// loaded by config.Load() before New() is called, so os.Getenv is reliable.
	if p := os.Getenv("MASTER_PROVIDER"); p != "" {
		m := os.Getenv("MASTER_MODEL")
		if m == "" {
			m = "default"
		}
		a.masterModel = p + "/" + m
	}
	a.chatBuf.WriteString(titleStyle.Render("ageni") + " — type a request to begin\n\n")
	a.refreshChat()
	a.refreshSide()
	return a
}

// LoadHistory renders prior conversation messages into the chat buffer
// so a resumed session shows where the user left off. Master tool calls
// + results are rendered the same way live events render them, so the
// resumed view is visually indistinguishable from a continuing session.
//
// Sub-agent activity is NOT replayed into the chat — workers were
// per-process and their transcripts didn't survive. The master's
// memory of what they returned is preserved through tool-result
// messages on its history.
func (a *App) LoadHistory(messages []llm.Message) {
	if len(messages) == 0 {
		return
	}
	a.chatBuf.WriteString(mutedStyle.Render(fmt.Sprintf("─── resumed: %d prior message(s) ───", len(messages))) + "\n\n")
	for _, m := range messages {
		switch m.Role {
		case llm.RoleUser:
			// Skip system-injected reminders / active_context blocks; users
			// don't need to see those replayed.
			if strings.HasPrefix(m.Text, "<active_context_block>") || strings.HasPrefix(m.Text, "<system-reminder>") || strings.HasPrefix(m.Text, "<session-resume>") {
				continue
			}
			a.chatBuf.WriteString(userStyle.Render("you ❯ ") + m.Text + "\n\n")
		case llm.RoleAssistant:
			if strings.TrimSpace(m.Text) != "" {
				a.chatBuf.WriteString(titleStyle.Render("master ❯") + "\n" + a.renderMarkdown(m.Text) + "\n\n")
			}
			for _, tc := range m.ToolCalls {
				a.chatBuf.WriteString(renderToolCall(tc.Name, tc.Arguments) + "\n")
			}
		case llm.RoleTool:
			for _, tr := range m.ToolResults {
				a.chatBuf.WriteString(renderToolResult(&tr) + "\n\n")
			}
		}
	}
	a.chatBuf.WriteString(mutedStyle.Render("─── continuing ───") + "\n\n")
	a.refreshChat()
	a.needsReorientation = true
}

// Tea Msg types
type busEvtMsg agent.Event
type usageMsg llm.TrackerSnapshot
type tickMsg time.Time
type reorientMsg struct{} // fired by Init when a session is resumed

// spinnerFrames is a 10-frame braille animation. Picked over dots because it
// reads as motion at the 120ms tick rate and renders correctly in a single
// terminal cell. Used for both the master "thinking" indicator and the
// per-sub-agent "running" marker.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinnerInterval = 120 * time.Millisecond

func tickCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (a *App) Init() tea.Cmd {
	cmds := []tea.Cmd{
		a.subscribeBus(),
		a.subscribeUsage(),
		textarea.Blink,
		tickCmd(),
	}
	if a.needsReorientation {
		cmds = append(cmds, func() tea.Msg { return reorientMsg{} })
	}
	return tea.Batch(cmds...)
}

// spinner returns the current animation frame; cycles independently of any
// particular activity so master and sub-agents pulse in sync.
func (a *App) spinner() string {
	return spinnerFrames[a.spinFrame%len(spinnerFrames)]
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

	// Mouse events (wheel scroll) always go to the chat viewport so the user
	// can scroll back through the session without losing input focus.
	if _, ok := msg.(tea.MouseMsg); ok {
		var cmd tea.Cmd
		a.chat, cmd = a.chat.Update(msg)
		return a, cmd
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// ── @ autocomplete navigation (takes priority over all other keys) ──
		if a.atComp != nil && a.atComp.active && a.focusInput {
			switch {
			case msg.Type == tea.KeyEsc:
				a.dismissAtComplete()
				// Also stop any in-flight generation — Esc means "stop everything".
				if a.masterBusy || a.masterToolIn != "" {
					a.stopGeneration()
				}
				return a, nil
			case msg.Type == tea.KeyUp:
				if a.atComp.sel > 0 {
					a.atComp.sel--
				}
				return a, nil
			case msg.Type == tea.KeyDown:
				if a.atComp.sel < len(a.atComp.matches)-1 {
					a.atComp.sel++
				}
				return a, nil
			case msg.Type == tea.KeyTab, msg.Type == tea.KeyEnter && !msg.Alt:
				a.insertAtCompletion()
				return a, nil
			}
		}

		switch {
		case msg.Type == tea.KeyCtrlC:
			a.cancel()
			return a, tea.Quit
		case msg.Type == tea.KeyTab:
			a.cycleView(1)
			return a, nil
		case msg.Type == tea.KeyShiftTab:
			a.cycleView(-1)
			return a, nil
		case msg.Type == tea.KeyEsc:
			a.stopGeneration()
			return a, nil
		case msg.String() == "ctrl+,", msg.String() == "ctrl+s":
			return a, a.openSettings()
		case msg.Type == tea.KeyF2:
			return a, a.toggleMouse()
		case msg.Type == tea.KeyF3:
			a.dumpSession()
			return a, nil
		case msg.Type == tea.KeyF4:
			a.dumpDiff()
			return a, nil
		case msg.Type == tea.KeyPgUp, msg.Type == tea.KeyPgDown,
			msg.String() == "ctrl+u", msg.String() == "ctrl+d":
			// Always route page-scroll keys to the chat viewport.
			var cmd tea.Cmd
			a.chat, cmd = a.chat.Update(msg)
			return a, cmd
		case msg.Type == tea.KeyUp && a.inputIsSingleLine():
			a.historyPrev()
			return a, nil
		case msg.Type == tea.KeyDown && a.inputIsSingleLine():
			a.historyNext()
			return a, nil
		}
		if msg.Type == tea.KeyEnter && !msg.Alt && a.focusInput {
			text := strings.TrimSpace(a.input.Value())
			if text != "" {
				a.input.Reset()
				a.historyIdx = -1
				a.historyDraft = ""
				a.dismissAtComplete()
				if a.history != nil {
					a.history.Append(text)
				}
				// Show the raw text in the chat (the user typed @path —
				// they don't want to see their screen flooded with the
				// resulting <attached_file> blocks). The master gets the
				// expanded form via masterIn.
				a.chatBuf.WriteString(userStyle.Render("you ❯ ") + a.wrapChat(text) + "\n\n")
				expanded, attached, skipped := expandFileMentions(text)
				if len(attached) > 0 {
					a.flashMessage = fmt.Sprintf("attached %d file(s): %s", len(attached), strings.Join(attached, ", "))
				}
				if len(skipped) > 0 {
					a.chatBuf.WriteString(mutedStyle.Render("[@-mentions skipped: "+strings.Join(skipped, ", ")+"]") + "\n\n")
				}
				if a.masterBusy || len(a.msgQueue) > 0 {
					// Master is processing — queue the message.
					a.msgQueue = append(a.msgQueue, expanded)
					a.chatBuf.WriteString(mutedStyle.Render(fmt.Sprintf("[queued — %d pending]", len(a.msgQueue))) + "\n\n")
				} else {
					a.currentMaster.Reset()
					a.masterBusy = true
					a.sendToMaster(expanded)
				}
				a.refreshChat()
				a.refreshSide()
				return a, nil
			}
		}

	case reorientMsg:
		const reorientText = "<session-resume>\n" +
			"The session was just resumed from a previous run. Review the conversation\n" +
			"history above and give a brief status update: (1) what was last accomplished,\n" +
			"(2) what was in progress or left incomplete, (3) what the immediate next step\n" +
			"should be. Then wait for the user's next instruction. Do NOT start new work\n" +
			"without being asked.\n" +
			"</session-resume>"
		a.chatBuf.WriteString(mutedStyle.Render("─── reorienting… ───") + "\n\n")
		a.refreshChat()
		a.masterBusy = true
		a.sendToMaster(reorientText)

	case relayMsg:
		a.handleEvent(agent.Event(msg.ev))
		cmds = append(cmds, a.subscribeOne(msg.sub))

	case relayUsageMsg:
		a.usage = a.renderUsageFromTracker()
		cmds = append(cmds, a.subscribeUsageOne(msg.sub))

	case tickMsg:
		a.spinFrame++
		// Re-render the side pane each tick so the running-sub-agent marker
		// animates. The chat pane also needs a redraw when the inline
		// "thinking…" indicator is showing — otherwise the spinner there
		// freezes. The status bar pulls from the same frame counter via the
		// View() call that follows this Update.
		if a.anyAnimationActive() {
			a.refreshSide()
			if a.shouldShowInlineIndicator() {
				a.refreshChat()
			}
		}
		cmds = append(cmds, tickCmd())
	}

	if a.focusInput {
		var cmd tea.Cmd
		a.input, cmd = a.input.Update(msg)
		cmds = append(cmds, cmd)
		// Re-evaluate @ autocomplete after every input update.
		if _, isKey := msg.(tea.KeyMsg); isKey {
			a.updateAtComplete()
		}
	} else {
		var cmd tea.Cmd
		a.chat, cmd = a.chat.Update(msg)
		cmds = append(cmds, cmd)
	}

	return a, tea.Batch(cmds...)
}

// inputIsSingleLine returns true when the input has no embedded newlines, so
// up/down arrow can safely be repurposed for command history without
// interfering with multi-line cursor movement.
func (a *App) inputIsSingleLine() bool {
	return !strings.Contains(a.input.Value(), "\n")
}

// historyPrev fills the input with the previous history entry. The first up
// press saves the current draft so the user can return to it via Down.
func (a *App) historyPrev() {
	if a.history == nil {
		return
	}
	items := a.history.Items()
	if len(items) == 0 {
		return
	}
	switch {
	case a.historyIdx == -1:
		a.historyDraft = a.input.Value()
		a.historyIdx = len(items) - 1
	case a.historyIdx > 0:
		a.historyIdx--
	default:
		return
	}
	a.input.SetValue(items[a.historyIdx])
	a.input.CursorEnd()
}

func (a *App) historyNext() {
	if a.history == nil || a.historyIdx == -1 {
		return
	}
	items := a.history.Items()
	a.historyIdx++
	if a.historyIdx >= len(items) {
		a.historyIdx = -1
		a.input.SetValue(a.historyDraft)
		a.historyDraft = ""
	} else {
		a.input.SetValue(items[a.historyIdx])
	}
	a.input.CursorEnd()
}

// updateAtComplete re-evaluates whether the @ autocomplete should be active,
// updates matches, and adjusts the layout if the suggestion-box height changed.
func (a *App) updateAtComplete() {
	li := a.input.LineInfo()
	prefix := inputTextUpToCursor(a.input.Value(), a.input.Line(), li.StartColumn, li.ColumnOffset)
	atByte, query, ok := detectAtPrefix(prefix)

	if !ok {
		if a.atComp != nil && a.atComp.active {
			a.atComp = nil
			a.layout()
		}
		return
	}

	cwd := getCwd()
	matches := matchFiles(cwd, query, atCompleteMaxItems)

	if a.atComp == nil {
		a.atComp = &atComplete{}
	}
	prevH := a.atComp.height()
	prevQuery := a.atComp.query
	a.atComp.active = true
	a.atComp.atByte = atByte
	if query != prevQuery {
		a.atComp.sel = 0
	}
	a.atComp.query = query
	a.atComp.matches = matches
	if len(matches) == 0 {
		a.atComp.active = false
	}

	if a.atComp.height() != prevH {
		a.layout()
	}
}

// insertAtCompletion replaces the @<query> token in the input with the
// selected path, then closes the suggestion panel.
func (a *App) insertAtCompletion() {
	if a.atComp == nil || !a.atComp.active {
		return
	}
	path := a.atComp.selectedPath()
	if path == "" {
		a.dismissAtComplete()
		return
	}
	val := a.input.Value()
	// Replace from atByte to atByte+1+len(query) with "@"+path
	end := a.atComp.atByte + 1 + len(a.atComp.query)
	if end > len(val) {
		end = len(val)
	}
	newVal := val[:a.atComp.atByte] + "@" + path + val[end:]
	a.input.SetValue(newVal)
	a.input.CursorEnd()
	a.dismissAtComplete()
}

// dismissAtComplete closes the suggestion panel without inserting.
func (a *App) dismissAtComplete() {
	if a.atComp != nil {
		hadHeight := a.atComp.height() > 0
		a.atComp = nil
		if hadHeight {
			a.layout()
		}
	}
}

// toggleMouse flips Bubble Tea's mouse capture so the user can drag-select
// text in the terminal and copy with the platform's native shortcut. When
// capture is off, mouse-wheel scrolling within the chat pane stops working
// — re-enable with F2 to get it back. Shift+drag bypasses capture in most
// modern terminals as a one-shot alternative.
func (a *App) toggleMouse() tea.Cmd {
	a.mouseOn = !a.mouseOn
	if a.mouseOn {
		a.flashMessage = "mouse capture ON — wheel scrolls chat"
		return tea.EnableMouseCellMotion
	}
	a.flashMessage = "mouse capture OFF — drag to select, F2 to resume"
	return tea.DisableMouse
}

// dumpSession writes a human-readable transcript of the current session
// (master + every sub-agent + tool calls + results) to a file in /tmp and
// flashes the path in the status bar so the user can grab it for debugging.
// Bound to F3.
func (a *App) dumpSession() {
	if a.session == nil {
		a.flashMessage = "dump: no session attached"
		return
	}
	path := filepath.Join(os.TempDir(), "ageni-session-"+a.session.ID+".txt")
	f, err := os.Create(path) //nolint:gosec
	if err != nil {
		a.flashMessage = "dump failed: " + err.Error()
		return
	}
	defer f.Close()
	if err := session.FormatLog(a.session, f); err != nil {
		a.flashMessage = "dump failed: " + err.Error()
		return
	}
	a.flashMessage = "dumped → " + path
}

// dumpDiff writes a unified diff for every recorded change in the
// current session to /tmp and flashes the path. Bound to F4.
// `ageni sessions diff <id>` is the equivalent CLI form.
func (a *App) dumpDiff() {
	if a.session == nil {
		a.flashMessage = "diff: no session attached"
		return
	}
	path := filepath.Join(os.TempDir(), "ageni-diff-"+a.session.ID+".diff")
	f, err := os.Create(path) //nolint:gosec
	if err != nil {
		a.flashMessage = "diff failed: " + err.Error()
		return
	}
	defer f.Close()
	if err := session.FormatDiff(a.session, "", f); err != nil {
		a.flashMessage = "diff failed: " + err.Error()
		return
	}
	a.flashMessage = "diff → " + path
}

func (a *App) stopGeneration() {
	if a.cancelInFlight == nil {
		return
	}
	queued := len(a.msgQueue)
	a.msgQueue = nil
	subs := a.cancelInFlight()
	// Reset busy state immediately — don't wait for EvMasterTurnDone, which
	// may never arrive if the master had nothing in-flight (e.g. a message
	// was queued in masterIn but the master hadn't started its turn yet).
	a.masterBusy = false
	a.masterToolIn = ""
	// Tell the master to discard any accumulated pending sub-agent events
	// so that cancelled workers don't re-trigger a new generation turn.
	select {
	case a.masterIn <- agent.Event{Kind: agent.EvCancelAll}:
	default:
	}
	a.refreshChat()
	a.refreshSide()
	switch {
	case subs > 0 && queued > 0:
		a.flashMessage = fmt.Sprintf("stopped generation (cancelled master + %d sub-agent(s); dropped %d queued message(s))", subs, queued)
	case subs > 0:
		a.flashMessage = fmt.Sprintf("stopped generation (cancelled master + %d sub-agent(s))", subs)
	case queued > 0:
		a.flashMessage = fmt.Sprintf("stopped generation (dropped %d queued message(s))", queued)
	default:
		a.flashMessage = "stopped generation"
	}
}

// sendToMaster sends a pre-expanded message text to the master inbox.
func (a *App) sendToMaster(expanded string) {
	select {
	case a.masterIn <- agent.Event{Kind: agent.EvUserMessage, Text: expanded}:
	default:
	}
}

// settingsHeaderLines is the number of lines rendered above the content in
// View() so we can pass the correct usable height to components.
const settingsHeaderLines = 3

func (a *App) openSettings() tea.Cmd {
	st, existing, err := newSettingsState()
	if err != nil {
		a.flashMessage = "settings: " + err.Error()
		return nil
	}
	a.settingsState = st
	a.settingsForm = nil
	a.settingsPhase = 0
	a.providerList = newProviderListModel(existing, a.width, a.height-settingsHeaderLines)
	a.mode = ModeSettings
	return a.providerList.Init()
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

	// Keep components sized to the usable height when the terminal is resized.
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		usable := ws.Height - settingsHeaderLines
		if usable > 0 {
			if a.providerList != nil {
				a.providerList.height = usable
			}
			if a.settingsForm != nil {
				a.settingsForm.WithHeight(usable)
			}
		}
	}

	// Phase 0 — provider list with inline key inputs.
	if a.settingsPhase == 0 {
		// Check for the "advance to next section" signal.
		if _, ok := msg.(providerListDoneMsg); ok {
			// Transfer provider selections into the state, then build the huh form.
			a.settingsState.applyProviderList(a.providerList)
			form, err := newSettingsFormFromState(a.settingsState, a.height-settingsHeaderLines)
			if err != nil {
				a.flashMessage = "settings: " + err.Error()
				a.mode = ModeChat
				return a, nil
			}
			a.settingsForm = form
			a.settingsPhase = 1
			return a, a.settingsForm.Init()
		}
		m, cmd := a.providerList.Update(msg)
		if pl, ok := m.(*providerListModel); ok {
			a.providerList = pl
		}
		return a, cmd
	}

	// Phase 1 — role selection / limits (huh form).
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
				msg := "settings applied — master: " + a.settingsState.masterProvider + "/" + a.settingsState.masterModel + ", sub-agent: " + a.settingsState.subProvider + "/" + a.settingsState.subModel
				// Keep status bar in sync with the newly applied model.
				a.masterModel = a.settingsState.masterProvider + "/" + a.settingsState.masterModel
				if vrs := a.settingsState.verifyResults; len(vrs) > 0 {
					failed := 0
					for _, vr := range vrs {
						if !strings.HasSuffix(vr, ": ok") {
							failed++
						}
					}
					if failed > 0 {
						msg += fmt.Sprintf("  (%d/%d providers verified)", len(vrs)-failed, len(vrs))
					} else {
						msg += fmt.Sprintf("  (all %d providers verified ✓)", len(vrs))
					}
					a.chatBuf.WriteString(mutedStyle.Render("[provider verification]") + "\n")
					for _, vr := range vrs {
						a.chatBuf.WriteString("  " + vr + "\n")
					}
					a.chatBuf.WriteString("\n")
				}
				a.flashMessage = msg
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

// cycleView steps through the sub-agent panes. dir=1 goes newest→oldest→master
// (matching the side-pane display order); dir=-1 reverses (Shift+Tab).
func (a *App) cycleView(dir int) {
	if len(a.subOrder) == 0 {
		return
	}
	n := len(a.subOrder)
	prevSub := a.viewSub
	if a.viewSub == "" {
		if dir >= 0 {
			// Tab: start at newest (last in subOrder)
			a.viewSub = a.subOrder[n-1]
		} else {
			// Shift+Tab: start at oldest (first in subOrder)
			a.viewSub = a.subOrder[0]
		}
	} else {
		idx := -1
		for i, id := range a.subOrder {
			if id == a.viewSub {
				idx = i
				break
			}
		}
		if idx < 0 {
			a.viewSub = ""
		} else if dir >= 0 {
			// Tab: newest first means stepping backwards through subOrder
			if idx == 0 {
				a.viewSub = ""
			} else {
				a.viewSub = a.subOrder[idx-1]
			}
		} else {
			// Shift+Tab: step forwards through subOrder (oldest→newest)
			if idx == n-1 {
				a.viewSub = ""
			} else {
				a.viewSub = a.subOrder[idx+1]
			}
		}
	}
	if a.viewSub != prevSub {
		// Reset to bottom when entering a new pane so the user always
		// lands on the latest output rather than inheriting the previous
		// pane's scroll position.
		a.refreshChatForce()
	} else {
		a.refreshChat()
	}
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
	suggestH := 0
	if a.atComp != nil {
		suggestH = a.atComp.height()
	}
	suggestHAdj := suggestH
	bodyH := a.height - inputH - statusH - 2 - suggestHAdj

	a.chat.Width = chatW
	a.chat.Height = max(bodyH, 3)
	a.side.Width = sideW
	a.side.Height = max(bodyH, 3)
	a.input.SetWidth(a.width - 4)
	a.input.SetHeight(inputH - 2)
	a.refreshChat()
	a.refreshSide()
}

func (a *App) handleEvent(ev agent.Event) {
	switch ev.Kind {
	case agent.EvMasterTurnStart:
		// LLM call is in flight. Indicator lights up regardless of who
		// triggered the turn (user submit, sub-agent completion, retry).
		a.masterBusy = true
		a.masterToolIn = ""
		a.currentReasoning.Reset()
		a.refreshSide()
	case agent.EvMasterReasoning:
		a.currentReasoning.WriteString(ev.Text)
		a.appendMasterRender()
	case agent.EvMasterText:
		// First real text token: commit the thinking block to the chat buffer
		// so it appears above the response in a collapsed muted section.
		if a.currentReasoning.Len() > 0 && a.currentMaster.Len() == 0 {
			a.flushReasoningBlock()
		}
		a.currentMaster.WriteString(ev.Text)
		a.appendMasterRender()
	case agent.EvMasterToolCall:
		if ev.ToolCall != nil {
			// Flush any reasoning block before the first tool call too.
			if a.currentReasoning.Len() > 0 {
				a.flushReasoningBlock()
			}
			a.flushMasterText() // commit any in-progress text before the tool block
			a.chatBuf.WriteString(renderToolCall(ev.ToolCall.Name, ev.ToolCall.Arguments) + "\n")
			a.masterToolIn = ev.ToolCall.Name
			a.pendingCalls[ev.ToolCall.ID] = *ev.ToolCall
			a.refreshChat()
			a.refreshSide()
		}
	case agent.EvMasterToolDone:
		if ev.ToolResult != nil {
			call, hasPending := a.pendingCalls[ev.ToolResult.ToolCallID]
			delete(a.pendingCalls, ev.ToolResult.ToolCallID)
			rendered := renderToolResult(ev.ToolResult)
			if hasPending && !ev.ToolResult.IsError {
				if diff := a.diffForCall(call); diff != "" {
					rendered += "\n" + diff
				}
			}
			a.chatBuf.WriteString(rendered + "\n\n")
			a.refreshChat()
		}
		a.masterToolIn = ""
		// todo_write may have mutated the list — refresh the side pane.
		a.refreshSide()
	case agent.EvMasterTurnDone:
		if a.currentReasoning.Len() > 0 {
			a.flushReasoningBlock()
		}
		a.flushMasterText()
		a.masterToolIn = ""
		if len(a.msgQueue) > 0 {
			next := a.msgQueue[0]
			a.msgQueue = a.msgQueue[1:]
			a.currentMaster.Reset()
			// masterBusy stays true — we're immediately dispatching the next queued message.
			a.sendToMaster(next)
		} else {
			a.masterBusy = false
		}
		a.refreshChat()
		a.refreshSide()
	case agent.EvSubagentSpawn:
		a.subBufs[ev.SubagentID] = &strings.Builder{}
		a.subStatus[ev.SubagentID] = agent.StatusRunning
		a.subActivity[ev.SubagentID] = "spawning"
		a.subOrder = append(a.subOrder, ev.SubagentID)
		header := titleStyle.Render(ev.SubagentID+" — "+ev.SubagentModel) + "\n" +
			styledLines(toolArgsStyle, ev.SubagentTask) + "\n\n"
		a.subBufs[ev.SubagentID].WriteString(header)
		a.refreshSide()
		a.refreshChat()
	case agent.EvSubagentTurnStart:
		a.subActivity[ev.SubagentID] = "thinking"
		a.refreshSide()
	case agent.EvSubagentText:
		// Stream raw to a per-sub-agent in-progress buffer; refreshChat
		// shows it live at the bottom of the sub-agent pane.
		cur := a.currentSubText[ev.SubagentID]
		if cur == nil {
			cur = &strings.Builder{}
			a.currentSubText[ev.SubagentID] = cur
		}
		cur.WriteString(ev.Text)
		a.refreshChat()
	case agent.EvSubagentToolCall:
		// A tool call ends the current text segment — flush the streamed
		// raw text into the persistent buffer with glamour rendering, then
		// write the styled tool block.
		a.flushSubText(ev.SubagentID)
		if b, ok := a.subBufs[ev.SubagentID]; ok && ev.ToolCall != nil {
			b.WriteString(renderToolCall(ev.ToolCall.Name, ev.ToolCall.Arguments) + "\n")
			a.subActivity[ev.SubagentID] = "tool:" + ev.ToolCall.Name
			a.pendingCalls[ev.ToolCall.ID] = *ev.ToolCall
		}
		a.refreshChat()
		a.refreshSide()
	case agent.EvSubagentToolDone:
		if ev.ToolResult != nil {
			call, hasPending := a.pendingCalls[ev.ToolResult.ToolCallID]
			delete(a.pendingCalls, ev.ToolResult.ToolCallID)
			if b, ok := a.subBufs[ev.SubagentID]; ok {
				rendered := renderToolResult(ev.ToolResult)
				if hasPending && !ev.ToolResult.IsError {
					if diff := a.diffForCall(call); diff != "" {
						rendered += "\n" + diff
					}
				}
				b.WriteString(rendered + "\n")
			}
			// Tool errors also bubble up to the master chat — easy to miss
			// in the sub-agent pane otherwise.
			if ev.ToolResult.IsError {
				a.chatBuf.WriteString(toolErrStyle.Render(fmt.Sprintf("[%s tool error] ", ev.SubagentID)) +
					toolErrStyle.Render(a.wrapChat(ev.ToolResult.Content)) + "\n")
			}
		}
		// Tool finished — back to "thinking" until the next stream-start
		// event, which will set it again (idempotent).
		a.subActivity[ev.SubagentID] = "thinking"
		a.refreshChat()
		// Sub-agent may have written to the shared todo list — refresh.
		a.refreshSide()
	case agent.EvSubagentRetry:
		// Surface transient errors + retries in chat so the user can see why
		// a sub-agent is taking longer than expected.
		a.chatBuf.WriteString(mutedStyle.Render(fmt.Sprintf("[%s retrying] %s", ev.SubagentID, a.wrapChat(ev.Text))) + "\n")
		if b, ok := a.subBufs[ev.SubagentID]; ok {
			b.WriteString("\n[retry] " + a.wrapChat(ev.Text) + "\n")
		}
		a.refreshChat()
	case agent.EvSubagentInbox:
		a.chatBuf.WriteString(mutedStyle.Render(fmt.Sprintf("[→ %s] %s", ev.SubagentID, a.wrapChat(ev.Text))) + "\n")
		if b, ok := a.subBufs[ev.SubagentID]; ok {
			b.WriteString("\n[inbox] " + a.wrapChat(ev.Text) + "\n")
		}
		a.refreshChat()
	case agent.EvSubagentDone:
		a.subStatus[ev.SubagentID] = agent.StatusDone
		delete(a.subActivity, ev.SubagentID)
		// ev.Text is the authoritative final assistant turn — drop any
		// in-progress streamed copy and render the canonical version once.
		if cur, ok := a.currentSubText[ev.SubagentID]; ok {
			cur.Reset()
		}
		if ev.Text != "" {
			if b, ok := a.subBufs[ev.SubagentID]; ok {
				rendered := a.renderMarkdown(ev.Text)
				b.WriteString("\n" + titleStyle.Render("final ❯") + "\n" + rendered + "\n")
			}
		}
		a.refreshSide()
		a.refreshChat()
	case agent.EvSubagentError:
		a.subStatus[ev.SubagentID] = agent.StatusError
		delete(a.subActivity, ev.SubagentID)
		errText := "(unknown)"
		if ev.Err != nil {
			errText = ev.Err.Error()
		}
		// Show the error in the master chat AND in the sub-agent's transcript
		// so the user sees it whether they're looking at master or sub-agent
		// view.
		a.chatBuf.WriteString(subErrStyle.Render(fmt.Sprintf("[%s error] %s", ev.SubagentID, a.wrapChat(errText))) + "\n")
		if b, ok := a.subBufs[ev.SubagentID]; ok {
			b.WriteString(fmt.Sprintf("\n[error] %s\n", a.wrapChat(errText)))
		}
		a.refreshSide()
		a.refreshChat()
	case agent.EvError:
		a.chatBuf.WriteString("\n" + subErrStyle.Render(fmt.Sprintf("[error] %v", a.wrapChat(ev.Err.Error()))) + "\n")
		a.masterToolIn = ""
		if len(a.msgQueue) > 0 {
			next := a.msgQueue[0]
			a.msgQueue = a.msgQueue[1:]
			a.currentMaster.Reset()
			a.sendToMaster(next)
		} else {
			a.masterBusy = false
		}
		a.refreshChat()
		a.refreshSide()
	case agent.EvFlash:
		a.flashMessage = ev.Text
	}
}

func (a *App) appendMasterRender() {
	// Live-render the in-progress master text as the trailing block.
	a.refreshChat()
}

// wrapChat uses lipgloss to word-wrap a string to the current chat-pane width.
func (a *App) wrapChat(s string) string {
	w := a.chat.Width - 2
	if w <= 0 {
		return s
	}
	return lipgloss.NewStyle().Width(w).Render(s)
}

// flushSubText commits the in-progress streamed text for a sub-agent into
// its persistent buffer, glamour-rendered. Called at every turn boundary
// (tool call or done) so each turn's text gets formatted markdown rendering.
func (a *App) flushSubText(id string) {
	cur, ok := a.currentSubText[id]
	if !ok || cur.Len() == 0 {
		return
	}
	rendered := a.renderMarkdown(cur.String())
	if b, ok := a.subBufs[id]; ok {
		b.WriteString(rendered + "\n\n")
	}
	cur.Reset()
}

func (a *App) flushMasterText() {
	if a.currentMaster.Len() == 0 {
		return
	}
	rendered := a.renderMarkdown(a.currentMaster.String())
	a.chatBuf.WriteString(titleStyle.Render("master ❯") + "\n" + rendered + "\n\n")
	a.currentMaster.Reset()
}

// flushReasoningBlock commits the accumulated thinking/reasoning content to
// the chat buffer as a muted collapsed block, then clears the buffer.
func (a *App) flushReasoningBlock() {
	if a.currentReasoning.Len() == 0 {
		return
	}
	text := a.currentReasoning.String()
	a.currentReasoning.Reset()
	// Wrap and dim the thinking content. Truncate very long blocks to keep
	// the chat readable — the full reasoning is available in the session log.
	const maxRunes = 1200
	runes := []rune(text)
	truncated := false
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
		truncated = true
	}
	wrapped := lipgloss.NewStyle().Width(a.chat.Width - 4).Render(string(runes))
	suffix := ""
	if truncated {
		suffix = mutedStyle.Render("… (truncated)")
	}
	block := mutedStyle.Render("⟨thinking⟩\n"+wrapped) + suffix + "\n" + mutedStyle.Render("⟨/thinking⟩") + "\n\n"
	a.chatBuf.WriteString(block)
}

// ensureGlamour rebuilds the markdown renderer when the chat-pane width
// changes. WithAutoStyle() can fall back to the no-tty profile inside
// Bubble Tea's alt-screen (resulting in unstyled output that looks like raw
// markdown), so we pick the style explicitly via lipgloss's background
// detection. Override with the GLAMOUR_STYLE env var if needed.
func (a *App) ensureGlamour() {
	w := a.chat.Width - 2
	if w < 20 {
		w = 80
	}
	if a.glam != nil && a.glamWidth == w {
		return
	}

	style := os.Getenv("GLAMOUR_STYLE")
	if style == "" {
		if lipgloss.HasDarkBackground() {
			style = "dark"
		} else {
			style = "light"
		}
	}

	// Force TrueColor profile. termenv's auto-detection inside Bubble Tea's
	// alt-screen sometimes returns Ascii (no escapes), which produces
	// unstyled markdown — which is exactly the symptom users report.
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(style),
		glamour.WithWordWrap(w),
		glamour.WithEmoji(),
		glamour.WithColorProfile(termenv.TrueColor),
	)
	if err != nil {
		// Fall back to auto in case the requested style is unknown.
		r, err = glamour.NewTermRenderer(
			glamour.WithAutoStyle(),
			glamour.WithWordWrap(w),
			glamour.WithColorProfile(termenv.TrueColor),
		)
		if err != nil {
			return
		}
	}
	a.glam = r
	a.glamWidth = w
}

// renderMarkdown formats a markdown string with glamour. On any failure it
// returns the input unchanged so output is never lost.
func (a *App) renderMarkdown(s string) string {
	a.ensureGlamour()
	if a.glam == nil {
		return s
	}
	out, err := a.glam.Render(s)
	if err != nil {
		return s
	}
	return strings.TrimRight(out, "\n")
}

// refreshChat updates the chat viewport, honouring the sticky-bottom
// behaviour: if the user was already at the bottom (following live output)
// we scroll to the new bottom; if they scrolled up to read history we leave
// the offset alone.
func (a *App) refreshChat() { a.setChat(a.chat.AtBottom()) }

// refreshChatForce unconditionally scrolls to the bottom of the new content.
// Used when switching panes so the user lands on the latest output rather than
// inheriting the previous pane's stale scroll offset.
func (a *App) refreshChatForce() { a.setChat(true) }

func (a *App) setChat(gotoBottom bool) {
	a.chat.SetContent(a.buildChatContent())
	if gotoBottom {
		a.chat.GotoBottom()
	}
}

// buildChatContent assembles the string to show in the chat viewport for the
// currently selected view (master or a specific sub-agent).
func (a *App) buildChatContent() string {
	if a.viewSub != "" {
		if b, ok := a.subBufs[a.viewSub]; ok {
			body := b.String()
			if cur, ok := a.currentSubText[a.viewSub]; ok && cur.Len() > 0 {
				body += titleStyle.Render(a.viewSub+" ❯ ") + cur.String()
			} else if line := a.subInlineIndicator(a.viewSub); line != "" {
				body += line
			}
			return body
		}
	}
	body := a.chatBuf.String()
	if a.currentReasoning.Len() > 0 && a.currentMaster.Len() == 0 {
		// Show live reasoning stream in a muted block before the response.
		body += mutedStyle.Render("⟨thinking⟩\n"+a.currentReasoning.String())
	}
	if a.currentMaster.Len() > 0 {
		body += titleStyle.Render("master ❯ ") + a.currentMaster.String()
	} else if a.currentReasoning.Len() == 0 {
		if line := a.masterInlineIndicator(); line != "" {
			body += line
		}
	}
	return body
}

// masterInlineIndicator renders a Claude-Code-style "✻ thinking…" line at
// the bottom of the chat pane while the master is generating but hasn't
// emitted any text yet (so the user has *something* moving on screen
// during the gap between LLM request and first byte). Returns "" when
// there's nothing to show.
func (a *App) masterInlineIndicator() string {
	switch {
	case a.masterToolIn != "":
		return mutedStyle.Render(fmt.Sprintf("%s master · running %s…", a.spinner(), a.masterToolIn))
	case a.masterBusy:
		return mutedStyle.Render(fmt.Sprintf("%s master · thinking…", a.spinner()))
	}
	return ""
}

// subInlineIndicator is the per-sub-agent equivalent: shown at the bottom
// of a sub-agent pane while the worker is alive but not currently streaming
// text. Reflects subActivity ("thinking" / "tool:NAME" / "spawning").
func (a *App) subInlineIndicator(id string) string {
	if a.subStatus[id] != agent.StatusRunning {
		return ""
	}
	act := a.subActivity[id]
	if act == "" {
		return ""
	}
	label := act
	if strings.HasPrefix(act, "tool:") {
		label = "running " + strings.TrimPrefix(act, "tool:") + "…"
	} else {
		label += "…"
	}
	return mutedStyle.Render(fmt.Sprintf("%s %s · %s", a.spinner(), id, label))
}

func (a *App) refreshSide() {
	var sb strings.Builder

	// Todos first — always at the top so they stay visible even when many
	// sub-agents are listed below them.
	if a.todo != nil {
		items := a.todo.Items()
		if len(items) > 0 {
			sb.WriteString(titleStyle.Render("todos") + "\n\n")
			frame0 := a.spinner()
			for _, it := range items {
				mark := "[ ]"
				style := mutedStyle
				switch it.Status {
				case tools.TodoInProgress:
					mark = "[" + frame0 + "]"
					style = subRunningStyle
				case tools.TodoCompleted:
					mark = "[✓]"
					style = subDoneStyle
				}
				owner := ""
				if it.ClaimedBy != "" {
					owner = mutedStyle.Render(" → " + it.ClaimedBy)
				}
				sb.WriteString(style.Render(fmt.Sprintf("%s %s", mark, it.Content)) + owner + "\n")
			}
			sb.WriteString("\n")
		}
	}

	sb.WriteString(titleStyle.Render("sub-agents") + "\n\n")
	if len(a.subOrder) == 0 {
		sb.WriteString(mutedStyle.Render("(none yet)\n"))
	}
	frame := a.spinner()
	// Newest first — iterate subOrder in reverse so the most recent agent
	// is at the top, closest to the todos section.
	for i := len(a.subOrder) - 1; i >= 0; i-- {
		id := a.subOrder[i]
		st := a.subStatus[id]
		marker := "•"
		st2 := subRunningStyle
		label := string(st)
		switch st {
		case agent.StatusRunning:
			marker = frame
			if act := a.subActivity[id]; act != "" {
				label = act
			}
		case agent.StatusDone:
			st2 = subDoneStyle
			marker = "✓"
		case agent.StatusError, agent.StatusCancelled:
			st2 = subErrStyle
			marker = "✗"
		}
		line := fmt.Sprintf("%s %s  %s", marker, id, label)
		if id == a.viewSub {
			line = lipgloss.NewStyle().Reverse(true).Render(line)
		}
		sb.WriteString(st2.Render(line) + "\n")
	}

	// Changed files — single source of truth for "what has this session
	// touched on disk". Refreshed on every refreshSide() call (cheap; just
	// reads the in-memory list). Most-recent first, capped to the side
	// pane's reasonable height.
	if a.changes != nil {
		summary := a.changes.Summary()
		if len(summary) > 0 {
			sb.WriteString("\n" + titleStyle.Render(fmt.Sprintf("changes (%d)", len(summary))) + "\n\n")
			max := 8
			start := 0
			if len(summary) > max {
				start = len(summary) - max
			}
			for i := len(summary) - 1; i >= start; i-- {
				c := summary[i]
				display := c.Path
				if cwd, err := os.Getwd(); err == nil {
					if rel, relErr := filepath.Rel(cwd, c.Path); relErr == nil && !strings.HasPrefix(rel, "..") {
						display = rel
					}
				}
				kindMark := "·"
				kindStyle := mutedStyle
				switch c.Kind {
				case tools.ChangeCreated:
					kindMark = "+"
					kindStyle = subDoneStyle
				case tools.ChangeEdited:
					kindMark = "~"
					kindStyle = subRunningStyle
				case tools.ChangeDeleted:
					kindMark = "-"
					kindStyle = subErrStyle
				case tools.ChangeMoved:
					kindMark = "→"
					kindStyle = subRunningStyle
				}
				sb.WriteString(kindStyle.Render(kindMark) + " " + display + "\n")
			}
			sb.WriteString(mutedStyle.Render("F4=dump diff") + "\n")
		}
	}

	a.side.SetContent(sb.String())
}

// shouldShowInlineIndicator returns true when the visible chat pane needs
// the inline "thinking…" line to be redrawn each tick. Master pane: any
// time the master is busy and has no in-progress text. Sub-agent pane:
// when the viewed sub-agent has activity but no streamed text yet.
func (a *App) shouldShowInlineIndicator() bool {
	if a.viewSub != "" {
		if a.subStatus[a.viewSub] != agent.StatusRunning {
			return false
		}
		if cur, ok := a.currentSubText[a.viewSub]; ok && cur.Len() > 0 {
			return false
		}
		return a.subActivity[a.viewSub] != ""
	}
	if a.currentMaster.Len() > 0 {
		return false
	}
	return a.masterBusy || a.masterToolIn != ""
}

// anyAnimationActive returns true when something on screen needs the spinner
// to advance — master generating, master tool in flight, or any sub-agent
// running. When everything is idle, ticks still fire (the loop never stops)
// but the side pane doesn't need re-rendering.
func (a *App) anyAnimationActive() bool {
	if a.masterBusy || a.masterToolIn != "" {
		return true
	}
	for _, st := range a.subStatus {
		if st == agent.StatusRunning {
			return true
		}
	}
	if a.todo != nil {
		for _, it := range a.todo.Items() {
			if it.Status == tools.TodoInProgress {
				return true
			}
		}
	}
	return false
}

func (a *App) View() string {
	if a.width < 60 || a.height < 12 {
		return "ageni: window too small (need 60×12)"
	}
	if a.mode == ModeSettings {
		header := titleStyle.Render("Settings") + statusStyle.Render("  Esc=cancel without saving\n\n")
		if a.settingsPhase == 0 && a.providerList != nil {
			return header + a.providerList.View()
		}
		if a.settingsForm != nil {
			sub := statusStyle.Render("Master → Sub-agent → Lead → Fallbacks → Limits   (Enter=advance  Tab=next field)\n\n")
			return header + sub + a.settingsForm.View()
		}
		return header
	}
	body := lipgloss.JoinHorizontal(lipgloss.Top,
		chatStyle.Height(a.chat.Height).Render(a.chat.View()),
		sideStyle.Height(a.side.Height).Render(a.side.View()),
	)
	in := inputStyle.Render(a.input.View())
	bottom := statusStyle.Render(a.statusLine())
	if a.atComp != nil && a.atComp.active && len(a.atComp.matches) > 0 {
		suggest := a.atComp.render(a.width)
		return lipgloss.JoinVertical(lipgloss.Left, body, suggest, in, bottom)
	}
	return lipgloss.JoinVertical(lipgloss.Left, body, in, bottom)
}

func (a *App) statusLine() string {
	view := "view: master"
	if a.viewSub != "" {
		view = "view: " + a.viewSub
	}
	state := a.masterStateLabel()
	model := ""
	if a.masterModel != "" {
		model = "  │  model: " + a.masterModel
	}
	sess := ""
	if a.session != nil && a.session.ID != "" {
		// Short prefix for the status bar; full ID is in the session log path.
		short := a.session.ID
		if len(short) > 13 {
			short = short[:13]
		}
		sess = "  │  s:" + short
	}
	flash := ""
	if a.flashMessage != "" {
		flash = "  │  " + a.flashMessage
	}
	mouseStr := "ON"
	if !a.mouseOn {
		mouseStr = "OFF"
	}
	queued := ""
	if len(a.msgQueue) > 0 {
		queued = fmt.Sprintf("  │  %d queued", len(a.msgQueue))
	}
	return fmt.Sprintf("%s%s  │  %s%s%s  │  %s  │  ↑↓=history  PgUp/PgDn=scroll  Tab/S-Tab=cycle  F2=mouse(%s)  F3=dump  F4=diff  Esc=stop  Ctrl+,=settings  Ctrl+C=quit%s", view, model, state, sess, queued, a.usage, mouseStr, flash)
}

// masterStateLabel returns a short string describing what the master is
// doing right now. Three observable states: thinking (LLM is generating or
// running a tool), waiting (master is between turns but at least one
// sub-agent is still running), idle (nothing in flight). The spinner frame
// is only emitted in the active states so the status bar visibly animates
// only when something's actually happening.
func (a *App) masterStateLabel() string {
	frame := a.spinner()
	switch {
	case a.masterToolIn != "":
		return frame + " master:" + a.masterToolIn + "…"
	case a.masterBusy:
		return frame + " master thinking…"
	}
	running := a.runningSubIDs()
	if len(running) > 0 {
		return frame + " master waiting on " + strings.Join(running, ",")
	}
	return mutedStyle.Render("master idle")
}

// runningSubIDs returns the IDs of sub-agents currently in StatusRunning,
// in the order they were spawned. Used by the status bar to tell the user
// who the master is waiting on.
func (a *App) runningSubIDs() []string {
	out := make([]string, 0, len(a.subOrder))
	for _, id := range a.subOrder {
		if a.subStatus[id] == agent.StatusRunning {
			out = append(out, id)
		}
	}
	return out
}

// renderUsageFromTracker shows master + sub-agent token usage with cache
// hit rate per role plus a session cost estimate. When free / local models
// did real work, the cost shows actual followed by indicative ("$0 / ≈$0.018
// paid") so the user can see what the same session would have cost on paid
// rates.
func (a *App) renderUsageFromTracker() string {
	master := a.tracker.StatsByRolePrefix("master")
	subs := a.tracker.StatsByRolePrefix("subagent:")
	actual, indicative, hasUnknown := a.tracker.SessionCostBreakdown()
	return fmt.Sprintf("%s  M:%s/%s c=%s  S:%s/%s c=%s",
		a.fmtCostBreakdown(actual, indicative, hasUnknown),
		fmtTokens(master.InputTokens+master.CacheReadTokens),
		fmtTokens(master.OutputTokens),
		fmtRate(master),
		fmtTokens(subs.InputTokens+subs.CacheReadTokens),
		fmtTokens(subs.OutputTokens),
		fmtRate(subs),
	)
}

// fmtCostBreakdown renders actual, optionally followed by the indicative
// paid-equivalent when the two diverge enough to be worth showing, and an
// inline "(saved $X)" tally for prompt caching savings.
func (a *App) fmtCostBreakdown(actual, indicative float64, hasUnknown bool) string {
	actualStr := fmtCost(actual, hasUnknown)
	gap := indicative - actual
	out := actualStr
	if gap > 0.01 || (actual > 0 && gap/actual > 0.1) {
		out = fmt.Sprintf("%s / ≈%s paid", actualStr, fmtCost(indicative, false))
	}
	if cacheSaved := a.tracker.SessionCacheSavings(); cacheSaved > 0.001 {
		out += " (saved " + fmtCost(cacheSaved, false) + " via cache)"
	}
	return out
}

// fmtCost renders a session cost. For very small amounts it falls back to
// 4 decimal places so the user can see fractions of a cent. The "?" prefix
// indicates that some tokens were spent on a model we don't have pricing
// for, so the figure is a floor.
func fmtCost(cost float64, hasUnknown bool) string {
	prefix := ""
	if hasUnknown {
		prefix = "≥"
	}
	switch {
	case cost == 0:
		return prefix + "$0"
	case cost < 0.01:
		return fmt.Sprintf("%s$%.4f", prefix, cost)
	case cost < 1:
		return fmt.Sprintf("%s$%.3f", prefix, cost)
	default:
		return fmt.Sprintf("%s$%.2f", prefix, cost)
	}
}

func fmtRate(s llm.RoleStats) string {
	if s.InputTokens+s.CacheReadTokens == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%%", s.CacheHitRate)
}

func fmtTokens(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ⏎ ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// fileMutationTools are the tool names whose completion should trigger a diff.
var fileMutationTools = map[string]bool{
	"write_file":  true,
	"edit_file":   true,
	"apply_diff":  true,
	"multi_edit":  true,
	"delete_file": true,
	"move_file":   true,
}

// diffForCall returns a rendered diff for a file-mutation tool call, or "".
func (a *App) diffForCall(call llm.ToolCall) string {
	if a.changes == nil {
		return ""
	}
	if !fileMutationTools[call.Name] {
		return ""
	}
	// Extract the file path from the tool arguments. All file tools use
	// "path" except move_file which writes to "dst".
	var args struct {
		Path string `json:"path"`
		Dst  string `json:"dst"`
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil || (args.Path == "" && args.Dst == "") {
		return ""
	}
	target := args.Path
	if target == "" {
		target = args.Dst
	}
	absPath, err := filepath.Abs(target)
	if err != nil {
		return ""
	}
	diff, err := a.changes.DiffFromSnapshot(absPath)
	if err != nil || diff == "" {
		return ""
	}
	return renderDiff(diff, diffMaxLines)
}
