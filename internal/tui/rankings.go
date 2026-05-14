package tui

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/bouwerp/ageni/internal/models"
)

// RankingsRefreshMsg is sent to the program when the background updater
// completes a fetch cycle and the rankings table should be re-rendered.
// Exported so cmd/ageni/main.go can send it via prog.Send.
type RankingsRefreshMsg struct{}

// rankingsRefreshMsg is the internal alias (same type).
type rankingsRefreshMsg = RankingsRefreshMsg

// rankingsModel is the full-screen model rankings dashboard.
// Rendered as a viewport containing a pre-built table string.
type rankingsModel struct {
	vp      viewport.Model
	width   int
	height  int
	content string // rendered table; rebuilt on resize and refresh
}

func newRankingsModel(width, height int) *rankingsModel {
	r := &rankingsModel{width: width, height: height}
	r.rebuild()
	usable := height - rankingsHeaderLines
	if usable < 1 {
		usable = 1
	}
	r.vp = viewport.New(width, usable)
	r.vp.SetContent(r.content)
	return r
}

const rankingsHeaderLines = 5 // title+hint · blank · subtitle · blank · column-header

func (r *rankingsModel) Update(msg tea.Msg) (*rankingsModel, tea.Cmd) {
	switch m := msg.(type) {
	case tea.WindowSizeMsg:
		r.width = m.Width
		r.height = m.Height
		usable := m.Height - rankingsHeaderLines
		if usable < 1 {
			usable = 1
		}
		r.vp.Width = m.Width
		r.vp.Height = usable
		r.rebuild()
		r.vp.SetContent(r.content)
	case rankingsRefreshMsg:
		r.rebuild()
		r.vp.SetContent(r.content)
	}
	var cmd tea.Cmd
	r.vp, cmd = r.vp.Update(msg)
	return r, cmd
}

func (r *rankingsModel) View() string {
	// Known provider abbreviations (display order).
	reg := models.Global
	updatedAt := reg.UpdatedAt()
	age := "never"
	if !updatedAt.IsZero() {
		d := time.Since(updatedAt).Round(time.Second)
		if d < time.Minute {
			age = fmt.Sprintf("%ds ago", int(d.Seconds()))
		} else {
			age = fmt.Sprintf("%dm ago", int(d.Minutes()))
		}
	}

	// Newlines must be OUTSIDE Render() calls: lipgloss pads every line in a
	// multi-line Render to the same width as the widest line, which causes the
	// blank lines to be filled with spaces. Those trailing spaces then
	// concatenate with the next content and shift it right.
	header := titleStyle.Render("Model Rankings") +
		statusStyle.Render("  Ctrl+R=close · ↑↓ scroll") +
		"\n\n"
	sub := statusStyle.Render(fmt.Sprintf(
		"Blended score: 60%% Aider-polyglot + 40%% curated baseline   │   Updated: %s",
		age,
	)) + "\n\n"
	colHeader := buildColHeader(r.width)
	return header + sub + colHeader + "\n" + r.vp.View()
}

// rebuild re-renders the table body from the current registry state.
func (r *rankingsModel) rebuild() {
	ranked := models.Global.Ranked()
	var sb strings.Builder
	for i, m := range ranked {
		if m.BlendedScore == 0 {
			continue
		}
		sb.WriteString(formatRow(i+1, m, r.width))
		sb.WriteByte('\n')
	}
	r.content = sb.String()
}

// Column widths (approximate; adjusted when terminal is narrow).
const (
	colRank    = 4
	colName    = 28
	colFamily  = 10
	colTier    = 9
	colScore   = 7
	colAider   = 8
	colCaps    = 5  // "V R" vision+reasoning flags
	colInCost  = 8  // "$1.25"
	colOutCost = 8  // "$10.0"
	colROI     = 7  // "123.4" or "—"
	// remainder → providers
)

func buildColHeader(width int) string {
	provW := width - colRank - colName - colFamily - colTier - colScore - colAider - colCaps - colInCost - colOutCost - colROI - 5
	if provW < 8 {
		provW = 8
	}
	return statusStyle.Render(
		padRight("#", colRank) +
			padRight("Model", colName) +
			padRight("Family", colFamily) +
			padRight("Tier", colTier) +
			padRight("Score", colScore) +
			padRight("Aider%", colAider) +
			padRight("Caps", colCaps) +
			padRight("In$/M", colInCost) +
			padRight("Out$/M", colOutCost) +
			padRight("ROI", colROI) +
			padRight("Providers", provW),
	)
}

// tierColor maps tier name to a lipgloss colour.
var tierColor = map[string]lipgloss.Color{
	"flagship": lipgloss.Color("220"), // gold
	"mid":      lipgloss.Color("39"),  // cyan
	"fast":     lipgloss.Color("245"), // grey
	"tiny":     lipgloss.Color("240"), // dark grey
}

func formatRow(rank int, m *models.CanonicalModel, width int) string {
	provW := width - colRank - colName - colFamily - colTier - colScore - colAider - colCaps - colInCost - colOutCost - colROI - 5
	if provW < 8 {
		provW = 8
	}

	rankStr := padRight(fmt.Sprintf("%d", rank), colRank)

	scoreStr := padRight(fmt.Sprintf("%.1f", m.BlendedScore), colScore)

	aiderScore := ""
	if v, ok := m.Scores["aider_polyglot"]; ok && v > 0 {
		aiderScore = fmt.Sprintf("%.1f%%", v)
	} else {
		aiderScore = "—"
	}

	// Tier with colour. Use lipgloss Width() so trailing-space padding is not
	// stripped by the renderer (lipgloss trims trailing spaces in Render).
	tierStr := m.Tier
	if c, ok := tierColor[m.Tier]; ok {
		tierStr = lipgloss.NewStyle().Foreground(c).Width(colTier).Render(m.Tier)
	} else {
		tierStr = padRight(tierStr, colTier)
	}

	// Capabilities: compact symbol flags — V=vision, R=reasoning.
	capsStr := formatCaps(m)

	// Cost and ROI columns.
	inCost := formatCost(m.InputCostPer1M)
	outCost := formatCost(m.OutputCostPer1M)
	roi := formatROI(m.ROIScore)

	// Providers: colored dots + name.
	provStr := buildProviderStr(m.AvailableProviders, provW)

	return rankStr +
		padRight(truncate(m.Name, colName-1), colName) +
		padRight(m.Family, colFamily) +
		tierStr +
		padRight(scoreStr, colScore) +
		padRight(aiderScore, colAider) +
		padRight(capsStr, colCaps) +
		padRight(inCost, colInCost) +
		padRight(outCost, colOutCost) +
		padRight(roi, colROI) +
		provStr
}

// formatCaps renders a compact capability string: "V" for vision, "R" for
// reasoning, "VR" for both, "—" for neither. Width is colCaps (5).
func formatCaps(m *models.CanonicalModel) string {
	vision := m.HasCapability("vision")
	reasoning := m.HasCapability("reasoning")
	switch {
	case vision && reasoning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("V") +
			lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Render("R")
	case vision:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("39")).Render("V")
	case reasoning:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("220")).Render("R")
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("—")
	}
}

// formatCost renders a per-million-token USD cost compactly:
// 0 → "—", <1 → "$0.15", ≥1 → "$3.0", ≥100 → "$150".
func formatCost(c float64) string {
	if c == 0 {
		return "—"
	}
	if c < 1.0 {
		return fmt.Sprintf("$%.2g", c)
	}
	if c < 10.0 {
		return fmt.Sprintf("$%.1f", c)
	}
	return fmt.Sprintf("$%.0f", c)
}

// formatROI renders an ROI score (BlendedScore / effective_cost_per_1M) compactly.
// 0 → "—", <1 → "0.63", <10 → "3.7", <100 → "45", ≥100 → "226".
func formatROI(r float64) string {
	if r == 0 {
		return "—"
	}
	if r < 1.0 {
		return fmt.Sprintf("%.2f", r)
	}
	if r < 10.0 {
		return fmt.Sprintf("%.1f", r)
	}
	return fmt.Sprintf("%.0f", r)
}


var knownProviderOrder = []string{
	"anthropic", "openai", "gemini", "deepseek",
	"groq", "cerebras", "mistral", "huggingface",
	"together", "openrouter", "opencode",
	"ollama", "ollama-cloud", "llamacpp", "vllm",
}

// providerColor maps provider name to a colour.
var providerColor = map[string]lipgloss.Color{
	"anthropic":    lipgloss.Color("208"),
	"openai":       lipgloss.Color("40"),
	"gemini":       lipgloss.Color("33"),
	"deepseek":     lipgloss.Color("134"),
	"groq":         lipgloss.Color("220"),
	"cerebras":     lipgloss.Color("202"),
	"mistral":      lipgloss.Color("171"),
	"huggingface":  lipgloss.Color("226"),
	"together":     lipgloss.Color("51"),
	"openrouter":   lipgloss.Color("39"),
	"opencode":     lipgloss.Color("118"),
	"ollama":       lipgloss.Color("243"),
	"ollama-cloud": lipgloss.Color("244"),
}

func buildProviderStr(providers []string, maxWidth int) string {
	if len(providers) == 0 {
		return statusStyle.Render("—")
	}
	// Sort by canonical display order.
	ordered := make([]string, 0, len(providers))
	added := map[string]bool{}
	for _, p := range knownProviderOrder {
		for _, avail := range providers {
			if avail == p && !added[p] {
				ordered = append(ordered, p)
				added[p] = true
			}
		}
	}
	// Append any not in the known order.
	for _, avail := range providers {
		if !added[avail] {
			ordered = append(ordered, avail)
		}
	}

	var parts []string
	used := 0
	for _, p := range ordered {
		badge := "● " + p
		if used+len(badge)+1 > maxWidth {
			parts = append(parts, "…")
			break
		}
		c, ok := providerColor[p]
		var rendered string
		if ok {
			rendered = lipgloss.NewStyle().Foreground(c).Render(badge)
		} else {
			rendered = badge
		}
		parts = append(parts, rendered)
		used += len(badge) + 1
	}
	return strings.Join(parts, " ")
}

// padRight pads/truncates s to exactly n visible characters (ASCII-safe).
func padRight(s string, n int) string {
	visible := utf8.RuneCountInString(s)
	if visible >= n {
		return string([]rune(s)[:n])
	}
	return s + strings.Repeat(" ", n-visible)
}

func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}
