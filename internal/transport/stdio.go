package transport

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
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
	name    string

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   *json.Encoder
	stdout  *bufio.Scanner
	started bool
	closed  bool

	// Hardening fields.
	restartCount    int
	maxRestarts     int
	lastRestartTime time.Time
	backoff         time.Duration
	permanentFail   bool
	startupTimeout  time.Duration
}

// NewStdioTransport creates a StdioTransport from the given stdio upstream config.
// The process is not started until the first request.
func NewStdioTransport(cfg *config.StdioUpstreamConfig) (*StdioTransport, error) {
	cmd := cfg.Command()
	if len(cmd) == 0 {
		return nil, fmt.Errorf("transport/stdio: command is required")
	}
	var env []string
	for k, v := range cfg.Env() {
		env = append(env, k+"="+v)
	}
	return &StdioTransport{
		command:        cmd,
		env:            env,
		name:           cfg.Name(),
		maxRestarts:    10,
		startupTimeout: 10 * time.Second,
	}, nil
}

// start spawns the child process and sets up stdin/stdout pipes.
// Must be called with mu held.
func (t *StdioTransport) start() error {
	t.cmd = exec.Command(t.command[0], t.command[1:]...)
	t.cmd.Env = append(os.Environ(), t.env...)

	// Capture stderr and pipe to logger at WARN level.
	stderrPipe, err := t.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("transport/stdio: stderr pipe: %w", err)
	}

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

	// Drain stderr in background, logging lines at WARN level.
	go t.drainStderr(stderrPipe)

	t.stdin = json.NewEncoder(stdinPipe)
	t.stdout = bufio.NewScanner(stdoutPipe)
	t.stdout.Buffer(make([]byte, 1024*1024), 1024*1024) // 1MB max line
	t.started = true
	return nil
}

func (t *StdioTransport) drainStderr(r io.ReadCloser) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		slog.Warn("upstream stderr", "upstream", t.name, "line", scanner.Text())
	}
}

// processAlive reports whether the child process is still running.
// Must be called with mu held.
func (t *StdioTransport) processAlive() bool {
	if !t.started || t.cmd == nil || t.cmd.Process == nil {
		return false
	}
	if t.cmd.ProcessState != nil {
		return false
	}
	err := t.cmd.Process.Signal(syscall.Signal(0))
	return err == nil
}

// ensureRunning starts the process if it hasn't been started, or restarts
// it if it has died. Must be called with mu held.
func (t *StdioTransport) ensureRunning() error {
	if t.permanentFail {
		return fmt.Errorf("transport/stdio: upstream %q permanently failed after %d restarts", t.name, t.maxRestarts)
	}
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
		if restartErr := t.restartWithBackoff(); restartErr != nil {
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
		if restartErr := t.restartWithBackoff(); restartErr != nil {
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

	t.ResetBackoff()
	return NewJSONResult(&resp), nil
}

// restartWithBackoff kills the current process and starts a new one,
// applying exponential backoff and respecting max restart limits.
// Must be called with mu held.
func (t *StdioTransport) restartWithBackoff() error {
	t.killProcess()

	t.restartCount++
	if t.restartCount > t.maxRestarts {
		t.permanentFail = true
		slog.Error("upstream permanently failed, max restarts exceeded",
			"upstream", t.name, "restarts", t.restartCount-1)
		return fmt.Errorf("transport/stdio: upstream %q permanently failed after %d restarts", t.name, t.maxRestarts)
	}

	// Compute backoff: 1s, 2s, 4s, 8s, ... up to 60s.
	if t.backoff == 0 {
		t.backoff = time.Second
	} else {
		t.backoff *= 2
		if t.backoff > 60*time.Second {
			t.backoff = 60 * time.Second
		}
	}

	slog.Warn("restarting upstream process",
		"upstream", t.name,
		"restart_count", t.restartCount,
		"backoff", t.backoff,
	)

	// Sleep for backoff (release mu briefly so Close can still proceed).
	t.mu.Unlock()
	time.Sleep(t.backoff)
	t.mu.Lock()

	if t.closed {
		return fmt.Errorf("transport/stdio: transport closed during restart backoff")
	}

	if err := t.start(); err != nil {
		return err
	}

	// Verify startup with a ping.
	return t.verifyStartup()
}

// verifyStartup sends a ping and waits for a response within the startup timeout.
// Must be called with mu held.
func (t *StdioTransport) verifyStartup() error {
	ping := &jsonrpc.Request{
		JSONRPC: jsonrpc.Version,
		Method:  "ping",
		ID:      jsonrpc.IntID(0),
	}

	// Use a timer for startup timeout.
	done := make(chan error, 1)
	go func() {
		if err := t.stdin.Encode(ping); err != nil {
			done <- fmt.Errorf("transport/stdio: startup ping write: %w", err)
			return
		}
		if !t.stdout.Scan() {
			if err := t.stdout.Err(); err != nil {
				done <- fmt.Errorf("transport/stdio: startup ping read: %w", err)
				return
			}
			done <- fmt.Errorf("transport/stdio: startup ping: unexpected EOF")
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		return err
	case <-time.After(t.startupTimeout):
		t.killProcess()
		return fmt.Errorf("transport/stdio: startup timeout (%v) for upstream %q", t.startupTimeout, t.name)
	}
}

// ResetBackoff resets the restart counter and backoff. Called when a request
// succeeds, indicating the process is stable.
func (t *StdioTransport) ResetBackoff() {
	t.restartCount = 0
	t.backoff = 0
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

// Healthy reports whether the child process is running and not permanently failed.
func (t *StdioTransport) Healthy() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.permanentFail {
		return false
	}
	return t.started && t.processAlive()
}
