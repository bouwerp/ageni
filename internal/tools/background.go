package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// jobStatus is the lifecycle state of a background shell job.
type jobStatus string

const (
	jobRunning  jobStatus = "running"
	jobDone     jobStatus = "done"
	jobFailed   jobStatus = "failed"
	jobKilled   jobStatus = "killed"
)

// backgroundJob holds a running or completed background shell process.
type backgroundJob struct {
	id      string
	command string
	started time.Time

	mu     sync.Mutex
	buf    bytes.Buffer // accumulated stdout+stderr
	status jobStatus
	exit   int
	cancel context.CancelFunc
}

func (j *backgroundJob) output() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.buf.String()
}

func (j *backgroundJob) appendOutput(b []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.buf.Len() < 128*1024 { // cap background job buffer at 128KB
		remaining := 128*1024 - j.buf.Len()
		if len(b) > remaining {
			b = b[:remaining]
		}
		j.buf.Write(b)
	}
}

func (j *backgroundJob) finish(exitCode int, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.exit = exitCode
	if err != nil && exitCode == 0 {
		j.status = jobFailed
	} else {
		j.status = jobDone
	}
}

// JobManager manages a set of background shell jobs.
type JobManager struct {
	mu      sync.Mutex
	jobs    map[string]*backgroundJob
	counter atomic.Int64
}

// NewJobManager creates a new JobManager.
func NewJobManager() *JobManager {
	return &JobManager{jobs: make(map[string]*backgroundJob)}
}

func (m *JobManager) newID() string {
	n := m.counter.Add(1)
	return fmt.Sprintf("job-%d", n)
}

// Start launches a command in the background and returns its job ID.
func (m *JobManager) Start(command string) (string, error) {
	if reason := checkDangerousCommand(command); reason != "" {
		return "", fmt.Errorf("blocked: %s — command not started", reason)
	}
	ctx, cancel := context.WithCancel(context.Background())
	id := m.newID()
	job := &backgroundJob{
		id:      id,
		command: command,
		started: time.Now(),
		status:  jobRunning,
		cancel:  cancel,
	}
	m.mu.Lock()
	m.jobs[id] = job
	m.mu.Unlock()

	go func() {
		cmd := exec.CommandContext(ctx, "bash", "-lc", command)
		pr, pw := io.Pipe()
		cmd.Stdout = pw
		cmd.Stderr = pw

		if err := cmd.Start(); err != nil {
			pw.Close()
			job.mu.Lock()
			job.status = jobFailed
			job.mu.Unlock()
			cancel()
			return
		}

		// Pump output into the job buffer.
		go func() {
			buf := make([]byte, 4096)
			for {
				n, err := pr.Read(buf)
				if n > 0 {
					job.appendOutput(buf[:n])
				}
				if err != nil {
					return
				}
			}
		}()

		err := cmd.Wait()
		pw.Close()

		exitCode := 0
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				exitCode = ee.ExitCode()
			}
		}
		job.finish(exitCode, err)
		cancel()
	}()

	return id, nil
}

// get returns a job by ID.
func (m *JobManager) get(id string) (*backgroundJob, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	return j, ok
}

// Kill cancels a background job.
func (m *JobManager) Kill(id string) error {
	j, ok := m.get(id)
	if !ok {
		return fmt.Errorf("no such job: %s", id)
	}
	j.cancel()
	j.mu.Lock()
	j.status = jobKilled
	j.mu.Unlock()
	return nil
}

// --- Tools ---

// RunBashBackground starts a shell command in the background and returns a job ID.
// The caller can poll with job_output and cancel with job_kill.
type RunBashBackground struct{ Jobs *JobManager }

func (RunBashBackground) Name() string { return "run_bash_background" }
func (RunBashBackground) Description() string {
	return "Start a bash command in the background. Returns a job_id immediately. Use job_output to read its output and job_kill to cancel it. Ideal for dev servers, long builds, test watchers, or any command you don't want to block on."
}
func (RunBashBackground) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"command":{"type":"string","description":"Bash command to run in background"}},"required":["command"]}`)
}
func (t RunBashBackground) Call(_ context.Context, args json.RawMessage) (string, error) {
	var p struct{ Command string }
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if strings.TrimSpace(p.Command) == "" {
		return "", errors.New("command is required")
	}
	id, err := t.Jobs.Start(p.Command)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("started background job %s: %s", id, p.Command), nil
}

// JobOutput reads the accumulated output of a background job.
type JobOutput struct{ Jobs *JobManager }

func (JobOutput) Name() string { return "job_output" }
func (JobOutput) Description() string {
	return "Read the current stdout+stderr of a background job started with run_bash_background. Also returns the job's current status (running/done/failed/killed) and exit code when finished."
}
func (JobOutput) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","description":"Job ID returned by run_bash_background"}},"required":["job_id"]}`)
}
func (t JobOutput) Call(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	j, ok := t.Jobs.get(p.JobID)
	if !ok {
		return "", fmt.Errorf("no such job: %s", p.JobID)
	}
	j.mu.Lock()
	status := j.status
	exit := j.exit
	elapsed := time.Since(j.started).Round(time.Second)
	j.mu.Unlock()

	out := j.output()
	if len(out) == 0 {
		out = "(no output yet)"
	}

	header := fmt.Sprintf("job %s | status: %s | elapsed: %s", p.JobID, status, elapsed)
	if status != jobRunning {
		header += fmt.Sprintf(" | exit: %d", exit)
	}
	return header + "\n---\n" + out, nil
}

// JobKill cancels a background job.
type JobKill struct{ Jobs *JobManager }

func (JobKill) Name() string { return "job_kill" }
func (JobKill) Description() string {
	return "Kill a background job started with run_bash_background. The process receives SIGKILL via context cancellation."
}
func (JobKill) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"job_id":{"type":"string","description":"Job ID returned by run_bash_background"}},"required":["job_id"]}`)
}
func (t JobKill) Call(_ context.Context, args json.RawMessage) (string, error) {
	var p struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return "", err
	}
	if err := t.Jobs.Kill(p.JobID); err != nil {
		return "", err
	}
	return fmt.Sprintf("killed job %s", p.JobID), nil
}
