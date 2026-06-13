package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/bouwerp/ageni/internal/agent"
	"github.com/bouwerp/ageni/internal/llm"
	"github.com/bouwerp/ageni/internal/session"
	"golang.org/x/term"
)

func runInteractiveCLI(
	ctx context.Context,
	bus *agent.Bus,
	master *agent.Master,
	manager *agent.Manager,
	masterIn chan agent.Event,
	sess *session.Session,
	resumeHistory []llm.Message,
	tracker *llm.Tracker,
) error {
	// Subscribe to events from the bus to print them to stdout
	events := bus.Subscribe(256)

	// Set up scanner for stdin
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Printf("\033[36mAgeni CLI Mode (Session: %s)\033[0m\n", sess.ID)
	fmt.Println("Type your prompt and press Enter. End a line with '\\' for multi-line inputs.")
	fmt.Println("Type '/help' to list available commands, '/settings' for settings, or '/exit' to exit.")
	fmt.Println()

	// If there is resumed history, notify the user
	if len(resumeHistory) > 0 {
		fmt.Printf("\033[90mResumed session history with %d message(s).\033[0m\n", len(resumeHistory))
	}

	// Initial status bar draw
	drawCLIStatusBar(sess.ID, tracker, len(manager.List()))

	// Ensure we restore terminal scrolling region on exit
	defer func() {
		fmt.Print("\033[r") // Reset scrolling region
		fmt.Print("\033[?25h") // Ensure cursor is visible
	}()

	// Read input and handle events concurrently
	for {
		// Draw status bar before prompting
		drawCLIStatusBar(sess.ID, tracker, len(manager.List()))

		prompt, ok := readPrompt(scanner)
		if !ok {
			break
		}
		if prompt == "exit" || prompt == "quit" {
			break
		}
		if strings.HasPrefix(prompt, "/") {
			if prompt == "/exit" || prompt == "/quit" {
				break
			}
			handleCLISlashCommand(prompt, master, manager, sess, tracker)
			continue
		}

		// Send user message to the master agent
		select {
		case masterIn <- agent.Event{Kind: agent.EvUserMessage, Text: prompt}:
		case <-ctx.Done():
			return ctx.Err()
		}

		// Read events from the bus and print them in real-time until the master's turn is done
		turnDone := false
		for !turnDone {
			// Update status bar when events are processed
			drawCLIStatusBar(sess.ID, tracker, len(manager.List()))

			select {
			case <-ctx.Done():
				return ctx.Err()
			case ev, ok := <-events:
				if !ok {
					return errors.New("event stream closed")
				}
				switch ev.Kind {
				case agent.EvMasterText:
					fmt.Print(ev.Text)
				case agent.EvMasterReasoning:
					// Print reasoning deltas in dim/gray color
					fmt.Printf("\033[90m%s\033[0m", ev.Text)
				case agent.EvMasterToolCall:
					if ev.ToolCall != nil {
						fmt.Printf("\n\033[36m[Master Tool: %s]\033[0m\n", ev.ToolCall.Name)
					}
				case agent.EvSubagentSpawn:
					fmt.Printf("\n\033[33m[Spawning Subagent %s: %s]\033[0m\n", ev.SubagentID, ev.Text)
				case agent.EvSubagentToolCall:
					if ev.ToolCall != nil {
						fmt.Printf("\033[33m[Subagent %s Tool: %s]\033[0m\n", ev.SubagentID, ev.ToolCall.Name)
					}
				case agent.EvSubagentDone:
					fmt.Printf("\033[32m[Subagent %s Done]\033[0m\n", ev.SubagentID)
				case agent.EvSubagentError:
					fmt.Printf("\033[31m[Subagent %s Failed: %v]\033[0m\n", ev.SubagentID, ev.Err)
				case agent.EvSubagentRetry:
					fmt.Printf("\033[33m[Subagent %s Retry: %s]\033[0m\n", ev.SubagentID, ev.Text)
				case agent.EvMasterTurnDone:
					fmt.Println()
					turnDone = true
				case agent.EvError:
					fmt.Printf("\n\033[31m[Error: %v]\033[0m\n", ev.Err)
					turnDone = true
				}
			}
		}
	}
	return nil
}

func readPrompt(scanner *bufio.Scanner) (string, bool) {
	var lines []string
	for {

		if len(lines) == 0 {
			fmt.Print("\nUser> ")
		} else {
			fmt.Print("    > ")
		}
		if !scanner.Scan() {
			return "", false
		}
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if len(lines) == 0 && (trimmed == "exit" || trimmed == "quit") {
			return trimmed, true
		}
		if strings.HasSuffix(line, "\\") {
			lines = append(lines, strings.TrimSuffix(line, "\\"))
			continue
		}
		lines = append(lines, line)
		break
	}
	return strings.Join(lines, "\n"), true
}

func drawCLIStatusBar(sessID string, tracker *llm.Tracker, activeWorkers int) {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || height < 5 || width < 10 {
		return
	}

	// Save cursor and attributes
	fmt.Print("\033[s")

	// Set scroll region: top to height-2
	// This keeps the bottom 2 lines static
	fmt.Printf("\033[1;%dr", height-2)

	// Move cursor to the static bottom bar
	// Line height-1: draw divider line or spacer
	fmt.Printf("\033[%d;1H", height-1)
	// Clear line
	fmt.Print("\033[2K")
	// Draw divider line
	divider := strings.Repeat("─", width)
	fmt.Printf("\033[90m%s\033[0m", divider)

	// Line height: draw status bar content
	fmt.Printf("\033[%d;1H", height)
	fmt.Print("\033[2K")

	// Collect status info
	var inputTokens, outputTokens int
	if tracker != nil {
		snap := tracker.Snapshot()
		inputTokens = snap.Total.InputTokens
		outputTokens = snap.Total.OutputTokens
	}

	// Format text bar content
	statusText := fmt.Sprintf(" 🤖 Session: %s | Workers: %d | Tokens: In %d / Out %d | Help: /help, /settings, /exit",
		sessID, activeWorkers, inputTokens, outputTokens)
	if len(statusText) > width-1 {
		statusText = statusText[:width-4] + "..."
	}
	// Styled status bar (cyan/black background or inverted color)
	fmt.Printf("\033[30;46m%s%s\033[0m", statusText, strings.Repeat(" ", width-len(statusText)))

	// Restore cursor
	fmt.Print("\033[u")
}

func handleCLISlashCommand(cmd string, master *agent.Master, manager *agent.Manager, sess *session.Session, tracker *llm.Tracker) {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return
	}
	op := strings.ToLower(parts[0])
	switch op {
	case "/help":
		fmt.Println("\n\033[36mAvailable Commands:\033[0m")
		fmt.Println("  /workers    - List active subagents and their states")
		fmt.Println("  /settings   - View and edit settings interactively")
		fmt.Println("  /clear      - Clear terminal screen")
		fmt.Println("  /help       - Show this help message")
		fmt.Println("  /exit, /quit- Exit ageni")
	case "/clear":
		fmt.Print("\033[H\033[2J") // Clear screen and home cursor
	case "/workers":
		subs := manager.List()
		if len(subs) == 0 {
			fmt.Println("\nNo active subagents.")
			return
		}
		fmt.Println("\n\033[36mActive Subagents:\033[0m")
		for _, s := range subs {
			fmt.Printf("  - %s [%s] Model: %s, Objective: %s\n", s.ID, s.Status(), s.Model, s.Task.Objective)
		}
	case "/settings":
		handleCLISettings(master, manager)
	default:
		fmt.Printf("\n\033[31mUnknown command: %s. Type /help for a list of commands.\033[0m\n", op)
	}
}

func handleCLISettings(master *agent.Master, manager *agent.Manager) {
	fmt.Println("\n\033[36m=== ageni Interactive Settings ===\033[0m")
	reader := bufio.NewReader(os.Stdin)

	for {
		fmt.Println("\nSelect setting to modify:")
		fmt.Printf("  1. Subagent Budget (Current: %d)\n", manager.DefaultBudget())
		fmt.Println("  2. Back to CLI")
		fmt.Print("\nChoice (1-2): ")

		choiceStr, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		choiceStr = strings.TrimSpace(choiceStr)
		if choiceStr == "2" || choiceStr == "" {
			break
		}

		if choiceStr == "1" {
			fmt.Print("Enter new subagent tool-call budget: ")
			budgetStr, err := reader.ReadString('\n')
			if err != nil {
				continue
			}
			budgetStr = strings.TrimSpace(budgetStr)
			var newBudget int
			_, err = fmt.Sscanf(budgetStr, "%d", &newBudget)
			if err != nil || newBudget <= 0 {
				fmt.Println("\033[31mInvalid budget value.\033[0m")
				continue
			}
			manager.SetDefaultBudget(newBudget)
			fmt.Printf("\033[32mSubagent budget updated to %d!\033[0m\n", newBudget)
		} else {
			fmt.Println("\033[31mInvalid choice.\033[0m")
		}
	}
}
