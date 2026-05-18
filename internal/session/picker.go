package session

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bouwerp/ageni/internal/homedir"
	"github.com/bouwerp/ageni/internal/tools"
	"github.com/charmbracelet/huh"
)

// pickerNew is the sentinel value returned for the "[new session]" choice.
const pickerNew = ""

// Pick presents an interactive list of recent sessions and returns the
// chosen session ID. An empty return value means the user picked "new
// session". If there are zero existing sessions, returns ("", nil)
// without prompting (the caller should fall through to creating a fresh
// session).
//
// Each entry shows last-used age, repo path, master+sub-agent models,
// and lightweight session statistics (todo count, change count) so the
// user can pick by context, not by ID.
func Pick() (string, error) {
	sessions, err := List()
	if err != nil {
		return "", err
	}
	if len(sessions) == 0 {
		return "", nil
	}

	options := make([]huh.Option[string], 0, len(sessions)+1)
	options = append(options, huh.NewOption("[new session]", pickerNew))
	for _, s := range sessions {
		options = append(options, huh.NewOption(formatSessionLabel(s), s.ID))
	}

	height := len(options) + 4
	if height > 18 {
		height = 18
	}

	var choice string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("Resume a session?").
				Description("↑↓ to navigate, Enter to pick, Esc to start fresh.").
				Options(options...).
				Value(&choice).
				Height(height),
		),
	)
	if err := form.Run(); err != nil {
		// Treat user abort (Esc) as "new session" — they bailed out, and
		// the most useful default is "I didn't want to resume".
		if errors.Is(err, huh.ErrUserAborted) {
			return pickerNew, nil
		}
		return "", err
	}
	return choice, nil
}

func formatSessionLabel(s *Session) string {
	parts := []string{s.ID, "·", humanise(s.LastUsed)}
	if s.RepoPath != "" {
		parts = append(parts, "·", shortenHome(s.RepoPath))
	}
	if model := joinModel(s.MasterProvider, s.MasterModel); model != "" {
		parts = append(parts, "·", model)
	}

	// Lightweight session stats — read from disk; cheap.
	var stats []string
	if todos := countTodos(s); todos > 0 {
		stats = append(stats, fmt.Sprintf("%d todo", todos))
	}
	if changes := countChanges(s); changes > 0 {
		stats = append(stats, fmt.Sprintf("%d change", changes))
	}
	if len(stats) > 0 {
		parts = append(parts, "·", strings.Join(stats, ", "))
	}
	return strings.Join(parts, " ")
}

func shortenHome(p string) string {
	if home, err := homedir.Dir(); err == nil {
		if rel, err := filepath.Rel(home, p); err == nil && !strings.HasPrefix(rel, "..") {
			return "~/" + rel
		}
	}
	return p
}

func joinModel(provider, model string) string {
	switch {
	case provider == "" && model == "":
		return ""
	case provider == "":
		return model
	case model == "":
		return provider
	}
	return provider + "/" + model
}

func countTodos(s *Session) int {
	t := tools.NewTodoWrite(s.Path("todo.json"))
	return len(t.Items())
}

func countChanges(s *Session) int {
	t := tools.NewChangeTracker(s.Path("changes.jsonl"), s.Path("snapshots"))
	return len(t.Summary())
}
