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
)

func runInteractiveCLI(
	ctx context.Context,
	bus *agent.Bus,
	master *agent.Master,
	manager *agent.Manager,
	masterIn chan agent.Event,
	sess *session.Session,
	resumeHistory []llm.Message,
) error {
	// Subscribe to events from the bus to print them to stdout
	events := bus.Subscribe(256)

	// Set up scanner for stdin
	scanner := bufio.NewScanner(os.Stdin)

	fmt.Printf("\033[36mAgeni CLI Mode (Session: %s)\033[0m\n", sess.ID)
	fmt.Println("Type your prompt and press Enter. End a line with '\\' for multi-line inputs.")
	fmt.Println("Type 'exit' or 'quit' to exit.")
	fmt.Println()

	// If there is resumed history, notify the user
	if len(resumeHistory) > 0 {
		fmt.Printf("\033[90mResumed session history with %d message(s).\033[0m\n", len(resumeHistory))
	}

	for {
		prompt, ok := readPrompt(scanner)
		if !ok {
			break
		}
		if prompt == "exit" || prompt == "quit" {
			break
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
