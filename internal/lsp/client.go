package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Client is a generic LSP client communicating with a language server subprocess.
type Client struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser

	mu        sync.Mutex
	nextID    int64
	pending   map[int64]chan<- *response
	onNotify  func(method string, params json.RawMessage)
	closeOnce sync.Once
	closeErr  error
	done      chan struct{}
}

type request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type notification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int64          `json:"id,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ResponseError  `json:"error,omitempty"`
}

// ResponseError is the standard LSP response error.
type ResponseError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("LSP error %d: %s", e.Code, e.Message)
}

// NewClient starts the command and wraps it in an LSP client.
func NewClient(cmd *exec.Cmd, onNotify func(method string, params json.RawMessage)) (*Client, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}

	c := &Client{
		cmd:      cmd,
		stdin:    stdin,
		stdout:   stdout,
		pending:  make(map[int64]chan<- *response),
		onNotify: onNotify,
		done:     make(chan struct{}),
	}
	go c.readLoop()
	return c, nil
}

// NewClientMock creates an in-memory client for testing.
func NewClientMock(stdin io.WriteCloser, stdout io.ReadCloser, onNotify func(method string, params json.RawMessage)) *Client {
	c := &Client{
		stdin:    stdin,
		stdout:   stdout,
		pending:  make(map[int64]chan<- *response),
		onNotify: onNotify,
		done:     make(chan struct{}),
	}
	go c.readLoop()
	return c
}

func (c *Client) write(msg any) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Write header
	_, err = fmt.Fprintf(c.stdin, "Content-Length: %d\r\n\r\n", len(data))
	if err != nil {
		return err
	}
	// Write payload
	_, err = c.stdin.Write(data)
	return err
}

func (c *Client) readLoop() {
	reader := bufio.NewReader(c.stdout)
	for {
		var contentLength int
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				c.handleClose(err)
				return
			}
			line = strings.TrimSpace(line)
			if line == "" {
				break
			}
			if strings.HasPrefix(strings.ToLower(line), "content-length:") {
				parts := strings.SplitN(line, ":", 2)
				if len(parts) == 2 {
					val := strings.TrimSpace(parts[1])
					contentLength, _ = strconv.Atoi(val)
				}
			}
		}

		if contentLength <= 0 {
			continue
		}

		body := make([]byte, contentLength)
		_, err := io.ReadFull(reader, body)
		if err != nil {
			c.handleClose(err)
			return
		}

		var base struct {
			Method string `json:"method"`
			ID     *int64 `json:"id"`
		}
		if err := json.Unmarshal(body, &base); err != nil {
			continue
		}

		if base.ID != nil {
			var resp response
			if err := json.Unmarshal(body, &resp); err == nil {
				c.handleResponse(&resp)
			}
		} else if base.Method != "" {
			var notif struct {
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal(body, &notif); err == nil {
				if c.onNotify != nil {
					c.onNotify(notif.Method, notif.Params)
				}
			}
		}
	}
}

func (c *Client) handleResponse(resp *response) {
	if resp.ID == nil {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[*resp.ID]
	c.mu.Unlock()
	if ok {
		ch <- resp
	}
}

func (c *Client) handleClose(err error) {
	c.closeOnce.Do(func() {
		c.closeErr = err
		close(c.done)
		_ = c.stdin.Close()
		_ = c.stdout.Close()
		if c.cmd != nil && c.cmd.Process != nil {
			_ = c.cmd.Process.Kill()
		}

		c.mu.Lock()
		for _, ch := range c.pending {
			close(ch)
		}
		c.pending = nil
		c.mu.Unlock()
	})
}

// Call makes a synchronous LSP request and decodes the response result into dst.
func (c *Client) Call(ctx context.Context, method string, params any, dst any) error {
	id := atomic.AddInt64(&c.nextID, 1)
	ch := make(chan *response, 1)

	c.mu.Lock()
	if c.pending == nil {
		c.mu.Unlock()
		return fmt.Errorf("client closed")
	}
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		if c.pending != nil {
			delete(c.pending, id)
		}
		c.mu.Unlock()
	}()

	req := request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	if err := c.write(req); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return fmt.Errorf("client closed: %w", c.closeErr)
	case resp := <-ch:
		if resp == nil {
			return fmt.Errorf("client closed during call")
		}
		if resp.Error != nil {
			return resp.Error
		}
		if dst != nil {
			return json.Unmarshal(resp.Result, dst)
		}
		return nil
	}
}

// Notify sends an asynchronous fire-and-forget notification to the language server.
func (c *Client) Notify(method string, params any) error {
	notif := notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return c.write(notif)
}

// Close terminates the language server and closes all pipes.
func (c *Client) Close() error {
	c.handleClose(io.EOF)
	if c.cmd != nil {
		return c.cmd.Wait()
	}
	return nil
}
