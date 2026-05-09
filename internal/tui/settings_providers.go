package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bouwerp/ageni/internal/llm"
)

// keyVerifyStatus tracks the async verification result for a provider's API key.
type keyVerifyStatus int

const (
	verifyUnknown keyVerifyStatus = iota
	verifyPending
	verifyOK
	verifyFail
)

// providerVerifyMsg is sent asynchronously when a key verification completes.
type providerVerifyMsg struct {
	name  string
	ok    bool
	errStr string
}

// providerListDoneMsg is emitted when the user presses Tab/Enter from the last
// row of the provider list, signalling that the settings page should advance to
// the next section (role selection / limits).
type providerListDoneMsg struct{}

// providerRow holds per-provider state shown in the list.
type providerRow struct {
	spec    llm.ProviderSpec
	enabled bool
	// input is only meaningful when spec.NeedsKey.
	input        textinput.Model
	editMode     bool // whether the key textinput currently has focus
	verifyStatus keyVerifyStatus
	verifyMsg    string // short error when verifyFail
}

// providerListModel is a Bubble Tea model that renders all providers as a
// scrollable list where each row has an inline checkbox and API key input.
//
// Navigation:
//   ↑ / ↓       — move cursor
//   Space        — toggle enabled / disabled
//   Tab / →      — if current row needs a key: enter key-edit mode
//                  otherwise: advance cursor (Tab from last row → providerListDoneMsg)
//   (in edit)    — type the API key; Esc / Enter / Tab to leave edit mode
//                  (Esc leaves edit mode only; a second Esc exits settings)
type providerListModel struct {
	rows    []providerRow
	cursor  int
	editing bool // true when the textinput on the cursor row is focused

	width  int
	height int
	scroll int // index of the first visible row
}

// newProviderListModel builds the model pre-populated from the existing env map.
func newProviderListModel(existing map[string]string, termWidth, termHeight int) *providerListModel {
	rows := make([]providerRow, 0, len(llm.AllProviders()))
	for _, p := range llm.AllProviders() {
		row := providerRow{spec: p}
		if p.NeedsKey {
			key := ""
			if p.APIKeyEnv != "" {
				key = existing[p.APIKeyEnv]
			}
			ti := textinput.New()
			ti.Placeholder = "paste or type API key…"
			ti.EchoMode = textinput.EchoPassword
			ti.CharLimit = 300
			ti.Width = 36
			if key != "" {
				ti.SetValue(key)
			}
			row.input = ti
			row.enabled = key != ""
		} else {
			// Local / no-key providers are always enabled.
			row.enabled = true
		}
		rows = append(rows, row)
	}
	return &providerListModel{
		rows:   rows,
		width:  termWidth,
		height: termHeight,
	}
}

// enabledNames returns the provider names that are currently checked.
func (m *providerListModel) enabledNames() []string {
	out := make([]string, 0, len(m.rows))
	for _, r := range m.rows {
		if r.enabled {
			out = append(out, r.spec.Name)
		}
	}
	return out
}

// keyValues returns a map of provider name → API key (may be empty string).
func (m *providerListModel) keyValues() map[string]string {
	out := make(map[string]string, len(m.rows))
	for _, r := range m.rows {
		if r.spec.NeedsKey {
			out[r.spec.Name] = r.input.Value()
		}
	}
	return out
}

func (m *providerListModel) Init() tea.Cmd { return nil }

func (m *providerListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tea.KeyMsg:
		if m.editing {
			return m.updateEditing(msg)
		}
		return m.updateNavigating(msg)
	}
	return m, nil
}

// updateEditing handles key messages while a textinput is focused.
func (m *providerListModel) updateEditing(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc, tea.KeyEnter:
		cmd := m.leaveEditMode()
		return m, cmd

	case tea.KeyTab:
		cmd := m.leaveEditMode()
		m.cursor++
		if m.cursor >= len(m.rows) {
			m.cursor = len(m.rows) - 1
			return m, tea.Batch(cmd, func() tea.Msg { return providerListDoneMsg{} })
		}
		m.clampScroll()
		return m, cmd

	default:
		var cmd tea.Cmd
		m.rows[m.cursor].input, cmd = m.rows[m.cursor].input.Update(msg)
		// A provider with a key entered counts as enabled.
		if m.rows[m.cursor].input.Value() != "" {
			m.rows[m.cursor].enabled = true
		}
		return m, cmd
	}
}

// updateNavigating handles key messages while browsing the row list.
func (m *providerListModel) updateNavigating(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyUp:
		if m.cursor > 0 {
			m.cursor--
			m.clampScroll()
		}
	case tea.KeyDown:
		if m.cursor < len(m.rows)-1 {
			m.cursor++
			m.clampScroll()
		}
	case tea.KeySpace:
		r := &m.rows[m.cursor]
		// Local providers without a key can't be disabled.
		if !r.spec.Local || r.spec.NeedsKey {
			r.enabled = !r.enabled
		}
	case tea.KeyTab, tea.KeyRight:
		r := &m.rows[m.cursor]
		if r.spec.NeedsKey {
			m.enterEditMode()
			return m, m.rows[m.cursor].input.Focus()
		}
		// No key needed — just advance.
		m.cursor++
		if m.cursor >= len(m.rows) {
			m.cursor = len(m.rows) - 1
			return m, func() tea.Msg { return providerListDoneMsg{} }
		}
		m.clampScroll()
	case tea.KeyShiftTab, tea.KeyLeft:
		if m.cursor > 0 {
			m.cursor--
			m.clampScroll()
		}
	case tea.KeyEnter:
		if m.cursor == len(m.rows)-1 {
			return m, func() tea.Msg { return providerListDoneMsg{} }
		}
		m.cursor++
		m.clampScroll()
	}
	return m, nil
}

func (m *providerListModel) enterEditMode() {
	m.editing = true
	m.rows[m.cursor].editMode = true
}

// leaveEditMode deactivates the textinput and, if a key was entered, fires an
// async goroutine to verify the key against the provider's endpoint.
func (m *providerListModel) leaveEditMode() tea.Cmd {
	i := m.cursor
	m.editing = false
	m.rows[i].editMode = false
	m.rows[i].input.Blur()

	key := m.rows[i].input.Value()
	spec := m.rows[i].spec
	if spec.NeedsKey && key != "" {
		m.rows[i].verifyStatus = verifyPending
		m.rows[i].verifyMsg = ""
		return func() tea.Msg {
			err := llm.VerifyKey(context.Background(), spec, key)
			if err != nil {
				return providerVerifyMsg{name: spec.Name, ok: false, errStr: err.Error()}
			}
			return providerVerifyMsg{name: spec.Name, ok: true}
		}
	}
	return nil
}

func (m *providerListModel) clampScroll() {
	visible := m.visibleRowCount()
	if m.cursor < m.scroll {
		m.scroll = m.cursor
	}
	if m.cursor >= m.scroll+visible {
		m.scroll = m.cursor - visible + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// visibleRowCount returns how many rows fit in the current height.
// Reserves 4 lines for the header / hint / scroll indicator.
func (m *providerListModel) visibleRowCount() int {
	h := m.height - 4
	if h < 3 {
		h = 3
	}
	return h
}

var (
	providerEnabledStyle  = lipgloss.NewStyle().Foreground(colorOK).Bold(true)
	providerCursorStyle   = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	providerKeyLabelStyle = lipgloss.NewStyle().Foreground(colorMuted)
	providerTagStyle      = lipgloss.NewStyle().Foreground(colorMuted)
	providerEditStyle     = lipgloss.NewStyle().Foreground(colorAccent)
)

func (m *providerListModel) View() string {
	var b strings.Builder

	// Header / hint line
	hint := "  ↑↓ move  ·  Space enable  ·  Tab/→ edit key  ·  Esc=save & exit  ·  Tab from last row → next section"
	b.WriteString(titleStyle.Render("Providers") + statusStyle.Render(hint) + "\n\n")

	visible := m.visibleRowCount()
	end := min(m.scroll+visible, len(m.rows))

	const labelWidth = 22

	for i := m.scroll; i < end; i++ {
		r := &m.rows[i]

		// Cursor marker
		if i == m.cursor {
			b.WriteString(providerCursorStyle.Render("▶ "))
		} else {
			b.WriteString("  ")
		}

		// Checkbox
		if r.enabled {
			b.WriteString(providerEnabledStyle.Render("●"))
		} else {
			b.WriteString(providerTagStyle.Render("○"))
		}
		b.WriteString(" ")

		// Provider label (fixed width so key column aligns)
		label := r.spec.Label
		if len(label) > labelWidth {
			label = label[:labelWidth]
		}
		b.WriteString(fmt.Sprintf("%-*s", labelWidth, label))
		b.WriteString(" ")

		// Tag
		tag := "paid "
		if r.spec.Free {
			tag = "free "
		}
		if r.spec.Local {
			tag = "local"
		}
		b.WriteString(providerTagStyle.Render("["+tag+"]"))

		// API key section
		if r.spec.NeedsKey {
			b.WriteString("  ")
			b.WriteString(providerKeyLabelStyle.Render("key "))
			if i == m.cursor && r.editMode {
				// Active text input
				b.WriteString(providerEditStyle.Render(r.input.View()))
			} else {
				val := r.input.Value()
				if val != "" {
					masked := strings.Repeat("●", min(len(val), 14))
					b.WriteString(mutedStyle.Render(masked + "  (set)"))
				} else {
					// Empty placeholder dashes
					b.WriteString(mutedStyle.Render("──────────────────────────"))
				}
			}
			// Verify badge (shown after key area)
			switch r.verifyStatus {
			case verifyPending:
				b.WriteString("  " + mutedStyle.Render("verifying…"))
			case verifyOK:
				b.WriteString("  " + lipgloss.NewStyle().Foreground(colorOK).Render("✓ ok"))
			case verifyFail:
				msg := r.verifyMsg
				if len(msg) > 40 {
					msg = msg[:37] + "…"
				}
				b.WriteString("  " + lipgloss.NewStyle().Foreground(colorErr).Render("✗ "+msg))
			}
		}

		b.WriteString("\n")
	}

	// Scroll indicator when there are more rows than fit.
	if len(m.rows) > visible {
		b.WriteString(mutedStyle.Render(fmt.Sprintf("\n  rows %d–%d of %d  (↑↓ to scroll)", m.scroll+1, end, len(m.rows))))
	}

	return b.String()
}
