// Package cmdutil provides a cross-platform utility for executing shell commands.
// It supports sync/async execution, stdin injection, real-time stdout/stderr streaming,
// and process lifecycle management (Start / Wait / Kill).
package cmdutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// ──────────────────────────────────────────────
// Errors
// ──────────────────────────────────────────────

var (
	ErrEmptyCommand = errors.New("cmdutil: command must not be empty")
	ErrNotStarted   = errors.New("cmdutil: process has not been started")
	ErrAlreadyDone  = errors.New("cmdutil: process already finished or killed")
)

// ──────────────────────────────────────────────
// Types
// ──────────────────────────────────────────────

// Mode controls how the command string is interpreted.
type Mode int

const (
	// ModeShell passes the command to the OS shell (cmd /C on Windows, sh -c elsewhere).
	ModeShell Mode = iota
	// ModeArgs treats the command as a space-separated argv slice (no shell expansion).
	ModeArgs
)

// Options configures a command execution.
type Options struct {
	// Timeout cancels the process after this duration (0 = no timeout).
	Timeout time.Duration

	// Ctx is an optional parent context. If nil, context.Background() is used.
	Ctx context.Context

	// Mode selects shell vs. args execution (default: ModeShell).
	Mode Mode

	// Env sets additional environment variables in "KEY=VALUE" format.
	// Merged on top of the current process environment.
	Env []string

	// Dir sets the working directory. Defaults to the current directory.
	Dir string

	// Stdin is attached to the subprocess's standard input.
	Stdin io.Reader

	// StdoutHandler is called for each chunk written to stdout (real-time streaming).
	// If nil, stdout is captured into Result.Stdout.
	StdoutHandler func(line []byte)

	// StderrHandler is called for each chunk written to stderr (real-time streaming).
	// If nil, stderr is captured into Result.Stderr.
	StderrHandler func(line []byte)
}

// Result holds the outcome of a completed command.
type Result struct {
	// Stdout contains captured output (empty when StdoutHandler is set).
	Stdout string
	// Stderr contains captured error output (empty when StderrHandler is set).
	Stderr string
	// ExitCode is the process exit code (-1 if the process was killed / timed out).
	ExitCode int
	// Duration is the wall-clock time the process ran.
	Duration time.Duration
}

// AsyncResult is delivered to the channel returned by RunAsync.
type AsyncResult struct {
	Result Result
	Err    error
}

// ──────────────────────────────────────────────
// Runner – stateless helpers
// ──────────────────────────────────────────────

// Run executes cmd synchronously and returns (Result, error).
func Run(cmd string, opts *Options) (Result, error) {
	if cmd == "" {
		return Result{}, ErrEmptyCommand
	}
	if opts == nil {
		opts = &Options{}
	}

	ctx, cancel := buildContext(opts)
	defer cancel()

	c, err := buildCmd(ctx, cmd, opts)
	if err != nil {
		return Result{}, err
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	c.Stdout = buildWriter(&stdoutBuf, opts.StdoutHandler)
	c.Stderr = buildWriter(&stderrBuf, opts.StderrHandler)
	if opts.Stdin != nil {
		c.Stdin = opts.Stdin
	}

	start := time.Now()
	runErr := c.Run()
	dur := time.Since(start)

	res := Result{
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: exitCode(c, runErr),
		Duration: dur,
	}

	if runErr != nil {
		return res, fmt.Errorf("cmdutil: command failed (exit %d): %w", res.ExitCode, runErr)
	}
	return res, nil
}

// RunAsync executes cmd asynchronously and returns a channel that emits exactly one AsyncResult.
func RunAsync(cmd string, opts *Options) <-chan AsyncResult {
	ch := make(chan AsyncResult, 1)
	go func() {
		res, err := Run(cmd, opts)
		ch <- AsyncResult{Result: res, Err: err}
		close(ch)
	}()
	return ch
}

// ──────────────────────────────────────────────
// Process – stateful lifecycle management
// ──────────────────────────────────────────────

// Process wraps an exec.Cmd and exposes Start / Wait / Kill lifecycle control,
// similar to a lightweight supervisor.
type Process struct {
	mu       sync.Mutex
	cmd      string
	opts     *Options
	execCmd  *exec.Cmd
	cancel   context.CancelFunc
	started  bool
	finished bool

	stdoutBuf bytes.Buffer
	stderrBuf bytes.Buffer

	startedAt time.Time
}

// NewProcess creates a Process but does not start it.
func NewProcess(cmd string, opts *Options) (*Process, error) {
	if cmd == "" {
		return nil, ErrEmptyCommand
	}
	if opts == nil {
		opts = &Options{}
	}
	return &Process{cmd: cmd, opts: opts}, nil
}

// Start launches the process in the background.
func (p *Process) Start() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.started {
		return errors.New("cmdutil: process already started")
	}

	ctx, cancel := buildContext(p.opts)
	p.cancel = cancel

	c, err := buildCmd(ctx, p.cmd, p.opts)
	if err != nil {
		cancel()
		return err
	}

	c.Stdout = buildWriter(&p.stdoutBuf, p.opts.StdoutHandler)
	c.Stderr = buildWriter(&p.stderrBuf, p.opts.StderrHandler)
	if p.opts.Stdin != nil {
		c.Stdin = p.opts.Stdin
	}

	if err := c.Start(); err != nil {
		cancel()
		return fmt.Errorf("cmdutil: start failed: %w", err)
	}

	p.execCmd = c
	p.started = true
	p.startedAt = time.Now()
	return nil
}

// Wait blocks until the process exits and returns its Result.
func (p *Process) Wait() (Result, error) {
	p.mu.Lock()
	if !p.started {
		p.mu.Unlock()
		return Result{}, ErrNotStarted
	}
	if p.finished {
		p.mu.Unlock()
		return Result{}, ErrAlreadyDone
	}
	c := p.execCmd
	startedAt := p.startedAt
	p.mu.Unlock()

	waitErr := c.Wait()
	dur := time.Since(startedAt)

	p.mu.Lock()
	p.finished = true
	if p.cancel != nil {
		p.cancel()
	}
	res := Result{
		Stdout:   p.stdoutBuf.String(),
		Stderr:   p.stderrBuf.String(),
		ExitCode: exitCode(c, waitErr),
		Duration: dur,
	}
	p.mu.Unlock()

	if waitErr != nil {
		return res, fmt.Errorf("cmdutil: wait failed (exit %d): %w", res.ExitCode, waitErr)
	}
	return res, nil
}

// Kill forcefully terminates the process.
func (p *Process) Kill() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.started {
		return ErrNotStarted
	}
	if p.finished {
		return ErrAlreadyDone
	}
	if p.cancel != nil {
		p.cancel()
	}
	if err := p.execCmd.Process.Kill(); err != nil {
		return fmt.Errorf("cmdutil: kill failed: %w", err)
	}
	p.finished = true
	return nil
}

// Pid returns the OS process id (0 if not started).
func (p *Process) Pid() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.execCmd == nil || p.execCmd.Process == nil {
		return 0
	}
	return p.execCmd.Process.Pid
}

// IsRunning reports whether the process was started and has not yet finished.
func (p *Process) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.started && !p.finished
}

// ──────────────────────────────────────────────
// Internal helpers
// ──────────────────────────────────────────────

// buildContext creates a context honouring both Ctx and Timeout options.
func buildContext(opts *Options) (context.Context, context.CancelFunc) {
	base := opts.Ctx
	if base == nil {
		base = context.Background()
	}
	if opts.Timeout > 0 {
		return context.WithTimeout(base, opts.Timeout)
	}
	return context.WithCancel(base)
}

// buildCmd constructs an exec.Cmd for the target OS.
//
//   - ModeShell (default): cmd /C <command>  on Windows
//                          sh  -c <command>  elsewhere
//   - ModeArgs: splits the command string by whitespace and uses argv directly.
func buildCmd(ctx context.Context, command string, opts *Options) (*exec.Cmd, error) {
	var c *exec.Cmd

	switch opts.Mode {
	case ModeArgs:
		args := splitArgs(command)
		if len(args) == 0 {
			return nil, ErrEmptyCommand
		}
		c = exec.CommandContext(ctx, args[0], args[1:]...)

	default: // ModeShell
		if runtime.GOOS == "windows" {
			c = exec.CommandContext(ctx, "cmd", "/C", command)
		} else {
			c = exec.CommandContext(ctx, "sh", "-c", command)
		}
	}

	if opts.Dir != "" {
		c.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		c.Env = append(os.Environ(), opts.Env...)
	}
	return c, nil
}

// buildWriter returns a writer that tees between a buffer and an optional handler.
func buildWriter(buf *bytes.Buffer, handler func([]byte)) io.Writer {
	if handler == nil {
		return buf
	}
	return &teeWriter{buf: buf, fn: handler}
}

// teeWriter writes to both a buffer and a callback.
type teeWriter struct {
	buf *bytes.Buffer
	fn  func([]byte)
}

func (t *teeWriter) Write(p []byte) (int, error) {
	t.fn(p)
	return t.buf.Write(p)
}

// exitCode extracts the integer exit code from exec.Cmd after completion.
func exitCode(c *exec.Cmd, runErr error) int {
	if c.ProcessState != nil {
		return c.ProcessState.ExitCode()
	}
	if runErr != nil {
		return -1
	}
	return 0
}

// splitArgs splits a command string into argv respecting quoted segments.
func splitArgs(s string) []string {
	var args []string
	var cur []byte
	inQuote := false
	quoteChar := byte(0)

	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case inQuote && ch == quoteChar:
			inQuote = false
		case !inQuote && (ch == '"' || ch == '\''):
			inQuote = true
			quoteChar = ch
		case !inQuote && ch == ' ':
			if len(cur) > 0 {
				args = append(args, string(cur))
				cur = cur[:0]
			}
		default:
			cur = append(cur, ch)
		}
	}
	if len(cur) > 0 {
		args = append(args, string(cur))
	}
	return args
}
