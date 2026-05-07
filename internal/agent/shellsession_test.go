package agent

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestShellSession_OpenExec(t *testing.T) {
	sm := NewShellManager(nil)
	defer sm.CancelAll()

	s, err := sm.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	out, err := s.Exec(context.Background(), "echo hello", 5*time.Second, true)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if !strings.Contains(out, "hello") {
		t.Errorf("expected 'hello' in output, got: %q", out)
	}
}

func TestShellSession_StateRetention(t *testing.T) {
	sm := NewShellManager(nil)
	defer sm.CancelAll()

	s, err := sm.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	_, err = s.Exec(context.Background(), "export MYVAR=foobar", 5*time.Second, true)
	if err != nil {
		t.Fatalf("set var: %v", err)
	}

	out, err := s.Exec(context.Background(), "echo $MYVAR", 5*time.Second, true)
	if err != nil {
		t.Fatalf("read var: %v", err)
	}
	if !strings.Contains(out, "foobar") {
		t.Errorf("expected 'foobar' in output, got: %q", out)
	}
}

func TestShellSession_AsyncExecWaitForPattern(t *testing.T) {
	sm := NewShellManager(nil)
	defer sm.CancelAll()

	s, err := sm.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	startOffset := s.TotalBytes()
	out, err := s.Exec(context.Background(), "sleep 0.1 && echo DONE_MARKER", 0, false)
	if err != nil {
		t.Fatalf("async exec: %v", err)
	}
	if out != "queued (async)" {
		t.Errorf("expected 'queued (async)', got: %q", out)
	}

	endOffset, err := s.WaitForPattern(context.Background(), "DONE_MARKER", startOffset, 10*time.Second)
	if err != nil {
		t.Fatalf("WaitForPattern: %v", err)
	}
	if endOffset <= startOffset {
		t.Errorf("expected endOffset > startOffset")
	}
}

func TestShellSession_IncrementalReadFrom(t *testing.T) {
	sm := NewShellManager(nil)
	defer sm.CancelAll()

	s, err := sm.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	_, err = s.Exec(context.Background(), "echo line1", 5*time.Second, true)
	if err != nil {
		t.Fatalf("exec 1: %v", err)
	}

	offset1 := s.TotalBytes()

	_, err = s.Exec(context.Background(), "echo line2", 5*time.Second, true)
	if err != nil {
		t.Fatalf("exec 2: %v", err)
	}

	data, nextOffset := s.ReadFrom(offset1, 8192)
	if nextOffset <= offset1 {
		t.Errorf("expected nextOffset > offset1")
	}
	if !strings.Contains(string(data), "line2") {
		t.Errorf("expected 'line2' in incremental read, got: %q", string(data))
	}
}

func TestShellManager_List(t *testing.T) {
	sm := NewShellManager(nil)
	defer sm.CancelAll()

	if len(sm.List()) != 0 {
		t.Error("expected empty list")
	}

	s1, _ := sm.Open()
	s2, _ := sm.Open()
	_ = s1
	_ = s2

	if len(sm.List()) != 2 {
		t.Errorf("expected 2 shells, got %d", len(sm.List()))
	}
}

func TestShellSession_CloseExecError(t *testing.T) {
	sm := NewShellManager(nil)
	defer sm.CancelAll()

	s, err := sm.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := sm.Close(s.ID()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = s.Exec(context.Background(), "echo hello", 5*time.Second, true)
	if err == nil {
		t.Error("expected error after close, got nil")
	}
}

func TestShellSession_SendInput(t *testing.T) {
	sm := NewShellManager(nil)
	defer sm.CancelAll()

	s, err := sm.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	startOffset := s.TotalBytes()

	// Start a read command that waits for stdin
	_, err = s.Exec(context.Background(), "read LINE && echo \"got: $LINE\"", 0, false)
	if err != nil {
		t.Fatalf("async read: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	if err := s.SendInput("testinput\n"); err != nil {
		t.Fatalf("SendInput: %v", err)
	}

	_, err = s.WaitForPattern(context.Background(), "got: testinput", startOffset, 10*time.Second)
	if err != nil {
		t.Fatalf("WaitForPattern: %v", err)
	}
}
