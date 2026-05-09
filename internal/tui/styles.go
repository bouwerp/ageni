package tui

import "github.com/charmbracelet/lipgloss"

var (
	colorBorder   = lipgloss.Color("240")
	colorBorderHi = lipgloss.Color("63")
	colorAccent   = lipgloss.Color("39")
	colorMuted    = lipgloss.Color("245")
	colorOK       = lipgloss.Color("42")
	colorErr      = lipgloss.Color("196")

	chatStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	sideStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	inputStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorBorder).
			Padding(0, 1)

	statusStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	titleStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	userStyle = lipgloss.NewStyle().
			Foreground(colorAccent).
			Bold(true)

	mutedStyle = lipgloss.NewStyle().Foreground(colorMuted)

	subRunningStyle = lipgloss.NewStyle().Foreground(colorAccent)
	subDoneStyle    = lipgloss.NewStyle().Foreground(colorOK)
	subErrStyle     = lipgloss.NewStyle().Foreground(colorErr)
)
