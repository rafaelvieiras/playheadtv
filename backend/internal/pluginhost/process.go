package pluginhost

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/rafaelvieiras/playheadtv/backend/internal/logging"
)

// ErrNotRunning is returned by a call on a process that already exited (or was
// never started).
var ErrNotRunning = errors.New("pluginhost: plugin process is not running")

// maxLineBytes bounds one protocol line. A plugin that writes a larger object
// than this is broken, and reading without a bound would let it exhaust memory.
const maxLineBytes = 8 << 20 // 8 MiB

// shutdownGrace is how long Close waits for a plugin to exit after `shutdown`
// and a closed stdin before killing it.
const shutdownGrace = 2 * time.Second

// Process is one running plugin: a child process speaking JSON-RPC over
// stdin/stdout (docs/PLUGINS.md "Protocol"). Calls are serialized — a plugin
// gets one request at a time — which keeps the implementation small and is
// enough for both hosts: the backend lists sources one after another, the
// client resolves a URL just before it plays.
type Process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr io.ReadCloser

	mu      sync.Mutex
	nextID  int64
	closed  bool
	exited  chan struct{}
	waitErr error

	// LogPrefix identifies the plugin in stderr lines the host logs.
	LogPrefix string
}

type rpcRequest struct {
	ID     int64  `json:"id"`
	Method string `json:"method"`
	Params any    `json:"params,omitempty"`
}

type rpcResponse struct {
	ID     int64           `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Error implements error for a plugin-reported failure, so callers can tell a
// plugin's refusal (ErrCode/Error()) from a transport failure.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return "plugin: " + e.Message
	}
	return fmt.Sprintf("plugin: %s: %s", e.Code, e.Message)
}

// Start launches the plugin at dir/entrypoint and performs the `info`
// handshake, so a returned Process is already known to speak this protocol.
func Start(ctx context.Context, dir string, manifest Manifest) (*Process, error) {
	exe := filepath.Join(dir, filepath.FromSlash(manifest.Entrypoint))
	if runtime.GOOS != "windows" {
		if err := os.Chmod(exe, 0o755); err != nil {
			return nil, fmt.Errorf("pluginhost: make entrypoint executable: %w", err)
		}
	}

	cmd := exec.Command(exe)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"PLAYHEADTV_PLUGIN_ID="+manifest.ID,
		"PLAYHEADTV_PLUGIN_VERSION="+manifest.Version,
		"PLAYHEADTV_PLUGIN_PROTOCOL="+fmt.Sprint(Protocol),
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("pluginhost: start %s: %w", manifest.ID, err)
	}

	p := &Process{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    bufio.NewReaderSize(stdout, 64<<10),
		stderr:    stderr,
		exited:    make(chan struct{}),
		LogPrefix: "plugin " + manifest.ID,
	}
	go p.collectStderr()
	go func() {
		p.waitErr = cmd.Wait()
		close(p.exited)
	}()

	var info Manifest
	infoCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	if err := p.Call(infoCtx, "info", nil, &info); err != nil {
		p.Close()
		return nil, fmt.Errorf("pluginhost: %s handshake: %w", manifest.ID, err)
	}
	if info.ID != manifest.ID || info.Version != manifest.Version || info.Protocol != Protocol {
		p.Close()
		return nil, fmt.Errorf("%w: %s reported id %q version %q protocol %d, installed %q %q",
			ErrIncompatible, manifest.ID, info.ID, info.Version, info.Protocol, manifest.ID, manifest.Version)
	}
	return p, nil
}

// collectStderr turns each stderr line into a debug log line, prefixed so the
// origin is obvious and never confused with the host's own output.
func (p *Process) collectStderr() {
	sc := bufio.NewScanner(p.stderr)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for sc.Scan() {
		logging.Debugf("%s: %s", p.LogPrefix, sc.Text())
	}
	_ = sc.Err()
}

// Call performs one request and decodes its result into result (which may be
// nil). A plugin-reported error comes back as *Error; anything else is a
// transport problem.
func (p *Process) Call(ctx context.Context, method string, params, result any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return ErrNotRunning
	}
	select {
	case <-p.exited:
		return ErrNotRunning
	default:
	}

	p.nextID++
	req := rpcRequest{ID: p.nextID, Method: method, Params: params}
	line, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("pluginhost: encode %s request: %w", method, err)
	}
	line = append(line, '\n')
	if _, err := p.stdin.Write(line); err != nil {
		select {
		case <-p.exited:
			return fmt.Errorf("%w (%s exited: %v)", ErrNotRunning, method, p.waitErr)
		default:
		}
		return fmt.Errorf("pluginhost: write %s request: %w", method, err)
	}

	// Reading a pipe has no deadline, so the only way to honor ctx is to kill
	// the process when it expires — which is exactly the documented contract
	// (a plugin that needs longer must page instead of blocking).
	watchdog := make(chan struct{})
	defer close(watchdog)
	go func() {
		select {
		case <-ctx.Done():
			p.killLocked()
		case <-watchdog:
		case <-p.exited:
		}
	}()

	raw, err := p.readLine()
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("pluginhost: %s timed out: %w", method, ctxErr)
		}
		select {
		case <-p.exited:
			return fmt.Errorf("%w (%s: %v)", ErrNotRunning, method, p.waitErr)
		default:
		}
		return fmt.Errorf("pluginhost: read %s response: %w", method, err)
	}

	var resp rpcResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("pluginhost: decode %s response: %w", method, err)
	}
	if resp.ID != req.ID {
		return fmt.Errorf("pluginhost: %s response id %d, want %d", method, resp.ID, req.ID)
	}
	if resp.Error != nil {
		return &Error{Code: resp.Error.Code, Message: resp.Error.Message}
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return fmt.Errorf("pluginhost: decode %s result: %w", method, err)
	}
	return nil
}

// readLine reads one protocol line, replacing the reader's default buffer with
// one bounded at maxLineBytes.
func (p *Process) readLine() ([]byte, error) {
	var buf []byte
	for {
		chunk, err := p.stdout.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxLineBytes {
			return nil, fmt.Errorf("response line exceeds %d bytes", maxLineBytes)
		}
		if err == nil {
			return buf[:len(buf)-1], nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return nil, err
	}
}

// killLocked stops the process. The caller holds p.mu (or is the watchdog that
// already does).
func (p *Process) killLocked() {
	select {
	case <-p.exited:
		return
	default:
	}
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
}

// Close asks the plugin to exit and kills it if it does not. Safe to call
// more than once, and safe to call while a Call is in flight: closing stdin is
// the polite half of the contract and the only half that cannot interleave
// with a read (a `shutdown` request would race the in-flight response).
func (p *Process) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.mu.Unlock()

	// A process that already exited needs nothing.
	select {
	case <-p.exited:
		return
	default:
	}

	_ = p.stdin.Close()
	select {
	case <-p.exited:
	case <-time.After(shutdownGrace):
		p.mu.Lock()
		p.killLocked()
		p.mu.Unlock()
	}
}
