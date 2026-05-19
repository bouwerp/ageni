package session

import (
	"strings"
	"testing"

	"github.com/bouwerp/ageni/internal/llm"
)

func TestLoadHistoryCompactsOlderReplayMessages(t *testing.T) {
	dir := t.TempDir()
	writeLogEntries(t, dir, []logEntry{
		{Kind: "user_message", Text: "u1"},
		{Kind: "master_text", Text: "a1"},
		{Kind: "master_turn_done"},
		{Kind: "user_message", Text: "u2"},
		{Kind: "master_text", Text: "a2"},
		{Kind: "master_turn_done"},
		{Kind: "user_message", Text: "u3"},
		{Kind: "master_text", Text: "a3"},
		{Kind: "master_turn_done"},
		{Kind: "user_message", Text: "u4"},
		{Kind: "master_text", Text: "a4"},
		{Kind: "master_turn_done"},
	})

	msgs, err := LoadHistory(&Session{Dir: dir})
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(msgs) != 7 {
		t.Fatalf("len(msgs) = %d, want 7", len(msgs))
	}
	if msgs[0].Role != llm.RoleUser || !strings.HasPrefix(msgs[0].Text, `<compacted_context source="replay">`) {
		t.Fatalf("first replay message = %+v, want compacted replay block", msgs[0])
	}
	if msgs[1].Role != llm.RoleUser || msgs[1].Text != "u2" {
		t.Fatalf("expected replay compaction to preserve whole exchange boundary, got %+v", msgs[1])
	}
}

func TestLoadHistoryLeavesShortReplayUnchanged(t *testing.T) {
	dir := t.TempDir()
	writeLogEntries(t, dir, []logEntry{
		{Kind: "user_message", Text: "u1"},
		{Kind: "master_text", Text: "a1"},
		{Kind: "master_turn_done"},
		{Kind: "user_message", Text: "u2"},
		{Kind: "master_text", Text: "a2"},
		{Kind: "master_turn_done"},
	})

	msgs, err := LoadHistory(&Session{Dir: dir})
	if err != nil {
		t.Fatalf("LoadHistory: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("len(msgs) = %d, want 4", len(msgs))
	}
	if strings.HasPrefix(msgs[0].Text, "<compacted_context") {
		t.Fatalf("did not expect replay compaction for short history: %+v", msgs)
	}
}
