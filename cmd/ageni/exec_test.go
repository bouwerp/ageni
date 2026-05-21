package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/bouwerp/ageni/internal/agent"
)

func TestParseExecPromptUsesArgs(t *testing.T) {
	got, err := parseExecPrompt([]string{"fix", "the", "build"}, strings.NewReader("ignored"), true)
	if err != nil {
		t.Fatalf("parseExecPrompt() error = %v", err)
	}
	if got != "fix the build" {
		t.Fatalf("parseExecPrompt() = %q, want %q", got, "fix the build")
	}
}

func TestParseExecPromptFallsBackToStdin(t *testing.T) {
	got, err := parseExecPrompt(nil, strings.NewReader(" investigate auth flow \n"), false)
	if err != nil {
		t.Fatalf("parseExecPrompt() error = %v", err)
	}
	if got != "investigate auth flow" {
		t.Fatalf("parseExecPrompt() = %q, want %q", got, "investigate auth flow")
	}
}

func TestParseExecPromptRejectsEmptyInput(t *testing.T) {
	if _, err := parseExecPrompt(nil, strings.NewReader(""), true); err == nil {
		t.Fatal("parseExecPrompt() error = nil, want usage error")
	}
}

func TestWaitForHeadlessResultReturnsTurnDone(t *testing.T) {
	events := make(chan agent.Event, 1)
	events <- agent.Event{Kind: agent.EvMasterTurnDone, Text: "done"}

	got, err := waitForHeadlessResult(context.Background(), events)
	if err != nil {
		t.Fatalf("waitForHeadlessResult() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("waitForHeadlessResult() = %q, want %q", got, "done")
	}
}

func TestWaitForHeadlessResultReturnsMasterError(t *testing.T) {
	wantErr := errors.New("boom")
	events := make(chan agent.Event, 1)
	events <- agent.Event{Kind: agent.EvError, Err: wantErr}

	_, err := waitForHeadlessResult(context.Background(), events)
	if !errors.Is(err, wantErr) {
		t.Fatalf("waitForHeadlessResult() error = %v, want %v", err, wantErr)
	}
}
