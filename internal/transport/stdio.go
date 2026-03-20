package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"

	"github.com/anguslmm/stile/internal/config"
	"github.com/anguslmm/stile/internal/jsonrpc"
)

var _ Transport = (*StdioTransport)(nil)

// StdioTransport implements Transport for MCP servers that communicate
// via stdin/stdout using line-delimited JSON-RPC.
type StdioTransport struct {
	command []string
	env     []string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   *json.Encoder
	stdout  *bufio.Scanner
	started bool
	closed  bool
}

// NewStdioTransport creates a StdioTransport from the given upstream config.
// The process is not started until the first request.
func NewStdioTransport(cfg config.UpstreamConfig) (*StdioTransport, error) {
	cmd := cfg.Command()
	if len(cmd) == 0 {
		return nil, fmt.Errorf("transport/stdio: command is required")
	}
	return &StdioTransport{
		command: cmd,
	}, nil
}

// start spawns the child process and sets up stdin/stdout pipes.
// Must be called with mu held.
func (t *StdioTransport) start() error {
	t.cmd = exec.Command(t.command[0], t.command[1:]...)
	t.cmd.Env = append(os.Environ(), t.env...)
	t.cmd.Stderr = os.Stderr

	stdinPipe, err := t.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("transport/stdio: stdin pipe: %w", err)
	}

	stdoutPipe, err := t.cmd.StdoutPipe()
	if err != nil {
		stdinPipe.Close()
		return fmt.Errorf("transport/stdio: stdout pipe: %w", err)
	}

	if err := t.cmd.Start(); err != nil {
		return fmt.Errorf("transport/stdio: start process: %w", err)
	}

	t.stdin = json.NewEncoder(stdinPipe)
	t.stdout = bufio.NewScanner(stdoutPipe)
	t.stdout.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line
	t.started = true
	return nil
}

// processAlive reports whether the child process is still running.
// Must be called with mu held.
func (t *StdioTransport) processAlive() bool {
	if !t.started || t.cmd == nil || t.cmd.Process == nil {
		return false
	}
	// A non-blocking check: if Wait has already been called or the process
	// has exited, ProcessState will be set.
	if t.cmd.ProcessState != nil {
		return false
	}
	// Try a zero signal to check if the process is alive.
	err := t.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

// ensureRunning starts the process if it hasn't been started, or restarts
// it if it has died. Must be called with mu held.
func (t *StdioTransport) ensureRunning() error {
	if t.started && t.processAlive() {
		return nil
	}
	// Clean up dead process state before restarting.
	if t.started {
		t.cleanup()
	}
	return t.start()
}

// cleanup waits for and cleans up a dead process without sending signals.
// Must be called with mu held.
func (t *StdioTransport) cleanup() {
	if t.cmd != nil && t.cmd.Process != nil {
		t.cmd.Wait() // reap zombie
	}
	t.started = false
	t.stdin = nil
	t.stdout = nil
	t.cmd = nil
}

// RoundTrip sends a JSON-RPC request over stdin and reads the response from stdout.
// Stdio is inherently sequential, so requests are serialized with a mutex.
func (t *StdioTransport) RoundTrip(_ context.Context, req *jsonrpc.Request) (TransportResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil, fmt.Errorf("transport/stdio: transport is closed")
	}

	if err := t.ensureRunning(); err != nil {
		return nil, err
	}

	// Write the request as a single line of JSON.
	if err := t.stdin.Encode(req); err != nil {
		// Process may have died. Try restart once.
		if restartErr := t.restart(); restartErr != nil {
			return nil, fmt.Errorf("transport/stdio: write request and restart failed: %w", restartErr)
		}
		if err := t.stdin.Encode(req); err != nil {
			return nil, fmt.Errorf("transport/stdio: write request after restart: %w", err)
		}
	}

	// Read one line of response.
	if !t.stdout.Scan() {
		scanErr := t.stdout.Err()
		// Process may have died. Try restart once and resend.
		if restartErr := t.restart(); restartErr != nil {
			if scanErr != nil {
				return nil, fmt.Errorf("transport/stdio: read response: %w", scanErr)
			}
			return nil, fmt.Errorf("transport/stdio: unexpected EOF from process")
		}
		// Resend after restart.
		if err := t.stdin.Encode(req); err != nil {
			return nil, fmt.Errorf("transport/stdio: write request after restart: %w", err)
		}
		if !t.stdout.Scan() {
			if err := t.stdout.Err(); err != nil {
				return nil, fmt.Errorf("transport/stdio: read response after restart: %w", err)
			}
			return nil, fmt.Errorf("transport/stdio: unexpected EOF from process after restart")
		}
	}

	line := t.stdout.Bytes()
	var resp jsonrpc.Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("transport/stdio: unmarshal response: %w", err)
	}

	return NewJSONResult(&resp), nil
}

// restart kills the current process and starts a new one.
// Must be called with mu held.
func (t *StdioTransport) restart() error {
	t.killProcess()
	return t.start()
}

// killProcess terminates the child process.
// Must be called with mu held.
func (t *StdioTransport) killProcess() {
	if t.cmd == nil || t.cmd.Process == nil {
		t.started = false
		return
	}

	// Send SIGTERM first.
	t.cmd.Process.Signal(syscall.SIGTERM)

	// Wait briefly for graceful exit.
	done := make(chan struct{})
	go func() {
		t.cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Process exited gracefully.
	case <-time.After(5 * time.Second):
		// Force kill.
		t.cmd.Process.Signal(syscall.SIGKILL)
		<-done
	}

	t.started = false
	t.stdin = nil
	t.stdout = nil
	t.cmd = nil
}

// Close shuts down the transport and kills the child process.
func (t *StdioTransport) Close() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.closed {
		return nil
	}
	t.closed = true

	if t.started {
		t.killProcess()
	}
	return nil
}

// Healthy reports whether the child process is running.
func (t *StdioTransport) Healthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.started && t.processAlive()
}
