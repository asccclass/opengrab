package cmdrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Mode string

const (
	ModeAuto  Mode = "auto"
	ModeArgs  Mode = "args"
	ModeShell Mode = "shell"
)

type Request struct {
	// auto | args | shell
	Mode Mode `json:"mode,omitempty"`

	// args mode
	Program string   `json:"program,omitempty"`
	Args    []string `json:"args,omitempty"`

	// shell mode
	ShellCommand string `json:"shell_command,omitempty"`

	// auto mode fallback
	Command string `json:"command,omitempty"`

	// optional working directory
	Dir string `json:"dir,omitempty"`

	// optional explanation from LLM for audit/debug
	Reason string `json:"reason,omitempty"`
}

type Result struct {
	Mode     Mode     `json:"mode"`
	Program  string   `json:"program,omitempty"`
	Args     []string `json:"args,omitempty"`
	Command  string   `json:"command,omitempty"`
	Shell    string   `json:"shell,omitempty"`
	PID      int      `json:"pid,omitempty"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	ExitCode int      `json:"exit_code"`
}

var (
	ErrEmptyRequest      = errors.New("empty request")
	ErrInvalidArgsMode   = errors.New("args mode requires program")
	ErrInvalidShellMode  = errors.New("shell mode requires shell_command")
	ErrInvalidMode       = errors.New("invalid execution mode")
	ErrEmptyProgram      = errors.New("program is empty")
	ErrEmptyShellCommand = errors.New("shell command is empty")
	ErrNotStarted        = errors.New("process not started")
	ErrAlreadyRunning    = errors.New("process is already running")
	ErrWaitTimeout       = errors.New("wait timeout")
)

type Options struct {
	// 預設為 ModeArgs
	Mode Mode

	// args 模式用
	Program string
	Args    []string

	// shell 模式用
	ShellCommand string

	Dir     string
	Env     []string
	Timeout time.Duration

	// stdin
	Stdin io.Reader

	// 即時串流
	Stdout io.Writer
	Stderr io.Writer
}

type AsyncResult struct {
	Result Result
	Err    error
}

type AsyncHandle struct {
	ResultChan <-chan AsyncResult
	Cancel     context.CancelFunc
}

type Process struct {
	parentCtx context.Context
	runCtx    context.Context
	cancel    context.CancelFunc
	opts      Options

	cmd *exec.Cmd

	startedAt time.Time

	stdoutBuf bytes.Buffer
	stderrBuf bytes.Buffer

	mu       sync.RWMutex
	result   Result
	err      error
	running  bool
	finished bool

	waitDone chan struct{}
	doneCh   chan AsyncResult
}

const ToolName = "cmdrunner"

const ToolDescription = `Execute commands with cmdrunner. Default to args mode. Use shell mode only when the command requires shell syntax such as redirection (> >> <), pipelines (|), chaining (&& ||), wildcard expansion, environment-variable expansion, or shell builtins.`

const ToolUsageGuide = `
Use args mode by default.

Choose args mode when the task can be expressed as one executable plus an explicit argument list, with no shell syntax.

Choose shell mode only when the command requires shell behavior, such as:
- redirection: > >> <
- pipelines: |
- chaining: && ||
- wildcard expansion: * ?
- environment-variable expansion
- shell builtins

For args mode, fill:
- program
- args

For shell mode, fill:
- shell_command

For auto mode, fill:
- command

Prefer args mode for safety and portability.
`

const ToolSchemaJSON = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "$id": "https://opengrab.local/tools/cmdrunner/cmdrunner.schema.json",
  "title": "CmdRunnerRequest",
  "description": "Schema for selecting args or shell execution in cmdrunner.",
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "mode": {
      "type": "string",
      "enum": ["auto", "args", "shell"],
      "default": "auto",
      "description": "Execution mode. Prefer args unless shell syntax is required."
    },
    "program": {
      "type": "string",
      "minLength": 1,
      "description": "Executable name for args mode."
    },
    "args": {
      "type": "array",
      "description": "Argument list for args mode.",
      "items": {
        "type": "string"
      },
      "default": []
    },
    "shell_command": {
      "type": "string",
      "minLength": 1,
      "description": "Raw command string for shell mode."
    },
    "command": {
      "type": "string",
      "minLength": 1,
      "description": "Raw command string for auto mode."
    },
    "dir": {
      "type": "string",
      "description": "Optional working directory."
    },
    "reason": {
      "type": "string",
      "description": "Optional explanation for why the mode was chosen."
    }
  },
  "oneOf": [
    {
      "title": "ArgsMode",
      "properties": {
        "mode": { "const": "args" }
      },
      "required": ["mode", "program"]
    },
    {
      "title": "ShellMode",
      "properties": {
        "mode": { "const": "shell" }
      },
      "required": ["mode", "shell_command"]
    },
    {
      "title": "AutoMode",
      "properties": {
        "mode": { "const": "auto" }
      },
      "required": ["mode"],
      "anyOf": [
        { "required": ["command"] },
        { "required": ["program"] },
        { "required": ["shell_command"] }
      ]
    }
  ],
  "examples": [
    {
      "mode": "args",
      "program": "go",
      "args": ["version"],
      "reason": "single executable with explicit arguments"
    },
    {
      "mode": "shell",
      "shell_command": "echo \"1234567\" > example.txt",
      "reason": "uses output redirection"
    },
    {
      "mode": "auto",
      "command": "cat file.txt | grep hello"
    }
  ]
}`

type ToolDefinition struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputSchema string `json:"input_schema"`
}

func GetToolDefinition() ToolDefinition {
	return ToolDefinition{
		Name:        ToolName,
		Description: ToolDescription,
		InputSchema: ToolSchemaJSON,
	}
}

func Run(ctx context.Context, req Request) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	mode, normalized, err := NormalizeRequest(req)
	if err != nil {
		return Result{}, err
	}

	switch mode {
	case ModeArgs:
		return runArgs(ctx, normalized)
	case ModeShell:
		return runShell(ctx, normalized)
	default:
		return Result{}, fmt.Errorf("unsupported mode: %s", mode)
	}
}

func NormalizeRequest(req Request) (Mode, Request, error) {
	if isEmpty(req) {
		return "", Request{}, ErrEmptyRequest
	}

	mode := req.Mode
	if mode == "" {
		mode = ModeAuto
	}

	switch mode {
	case ModeArgs:
		if strings.TrimSpace(req.Program) == "" {
			return "", Request{}, ErrInvalidArgsMode
		}
		return ModeArgs, Request{
			Mode:    ModeArgs,
			Program: req.Program,
			Args:    cloneStrings(req.Args),
			Dir:     req.Dir,
			Reason:  req.Reason,
		}, nil

	case ModeShell:
		if strings.TrimSpace(req.ShellCommand) == "" {
			return "", Request{}, ErrInvalidShellMode
		}
		return ModeShell, Request{
			Mode:         ModeShell,
			ShellCommand: req.ShellCommand,
			Dir:          req.Dir,
			Reason:       req.Reason,
		}, nil

	case ModeAuto:
		// If the caller already provided Program/Args, prefer args mode.
		if strings.TrimSpace(req.Program) != "" {
			return ModeArgs, Request{
				Mode:    ModeArgs,
				Program: req.Program,
				Args:    cloneStrings(req.Args),
				Dir:     req.Dir,
				Reason:  chooseReason(req.Reason, "auto resolved to args because program was explicitly provided"),
			}, nil
		}

		// If the caller already provided ShellCommand, prefer shell mode.
		if strings.TrimSpace(req.ShellCommand) != "" {
			return ModeShell, Request{
				Mode:         ModeShell,
				ShellCommand: req.ShellCommand,
				Dir:          req.Dir,
				Reason:       chooseReason(req.Reason, "auto resolved to shell because shell_command was explicitly provided"),
			}, nil
		}

		cmd := strings.TrimSpace(req.Command)
		if cmd == "" {
			return "", Request{}, ErrEmptyRequest
		}

		if NeedsShell(cmd, runtime.GOOS) {
			return ModeShell, Request{
				Mode:         ModeShell,
				ShellCommand: cmd,
				Dir:          req.Dir,
				Reason:       chooseReason(req.Reason, "auto resolved to shell because command requires shell syntax"),
			}, nil
		}

		program, args, err := SplitCommandLine(cmd)
		if err != nil {
			return ModeShell, Request{
				Mode:         ModeShell,
				ShellCommand: cmd,
				Dir:          req.Dir,
				Reason:       chooseReason(req.Reason, "auto fell back to shell because command line could not be safely split"),
			}, nil
		}

		return ModeArgs, Request{
			Mode:    ModeArgs,
			Program: program,
			Args:    args,
			Dir:     req.Dir,
			Reason:  chooseReason(req.Reason, "auto resolved to args because command is a simple executable plus arguments"),
		}, nil

	default:
		return "", Request{}, fmt.Errorf("invalid mode: %s", mode)
	}
}

func NeedsShell(command, goos string) bool {
	s := strings.TrimSpace(command)
	if s == "" {
		return false
	}

	if hasUnquotedShellMeta(s) {
		return true
	}

	first := strings.ToLower(firstToken(s))
	if first == "" {
		return false
	}

	posixBuiltins := map[string]struct{}{
		"cd": {}, "export": {}, "source": {}, ".": {}, "alias": {}, "unalias": {},
		"set": {}, "unset": {}, "eval": {}, "exec": {}, "trap": {}, "umask": {},
		"for": {}, "while": {}, "if": {}, "then": {}, "fi": {}, "case": {}, "function": {},
	}

	windowsBuiltins := map[string]struct{}{
		"cd": {}, "dir": {}, "copy": {}, "del": {}, "erase": {}, "move": {},
		"ren": {}, "rename": {}, "set": {}, "type": {}, "cls": {}, "echo": {},
	}

	if goos == "windows" {
		_, ok := windowsBuiltins[first]
		return ok
	}

	_, ok := posixBuiltins[first]
	return ok
}

func SplitCommandLine(s string) (string, []string, error) {
	var parts []string
	var buf strings.Builder

	inSingle := false
	inDouble := false
	escaped := false

	flush := func() {
		if buf.Len() > 0 {
			parts = append(parts, buf.String())
			buf.Reset()
		}
	}

	for _, r := range s {
		if escaped {
			buf.WriteRune(r)
			escaped = false
			continue
		}

		switch r {
		case '\\':
			if inSingle {
				buf.WriteRune(r)
			} else {
				escaped = true
			}

		case '\'':
			if inDouble {
				buf.WriteRune(r)
			} else {
				inSingle = !inSingle
			}

		case '"':
			if inSingle {
				buf.WriteRune(r)
			} else {
				inDouble = !inDouble
			}

		case ' ', '\t', '\n':
			if inSingle || inDouble {
				buf.WriteRune(r)
			} else {
				flush()
			}

		default:
			buf.WriteRune(r)
		}
	}

	if escaped || inSingle || inDouble {
		return "", nil, errors.New("unclosed quote or dangling escape")
	}

	flush()

	if len(parts) == 0 {
		return "", nil, ErrEmptyRequest
	}

	return parts[0], parts[1:], nil
}

func FirstToken(s string) string {
	return firstToken(s)
}

func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var e *exec.ExitError
	if errors.As(err, &e) {
		return e.ExitCode()
	}
	return -1
}

func JoinCommand(program string, args []string) string {
	if len(args) == 0 {
		return program
	}
	return program + " " + strings.Join(args, " ")
}

func runArgs(ctx context.Context, req Request) (Result, error) {
	cmd := exec.CommandContext(ctx, req.Program, req.Args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()

	res := Result{
		Mode:     ModeArgs,
		Program:  req.Program,
		Args:     cloneStrings(req.Args),
		Command:  JoinCommand(req.Program, req.Args),
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: ExitCode(err),
	}

	if err != nil {
		return res, fmt.Errorf("args execution failed: %w", err)
	}

	return res, nil
}

func runShell(ctx context.Context, req Request) (Result, error) {
	shell, shellArgs := platformShell()
	args := append(shellArgs, req.ShellCommand)

	cmd := exec.CommandContext(ctx, shell, args...)
	if req.Dir != "" {
		cmd.Dir = req.Dir
	}

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer
	cmd.Stdout = &stdoutBuf
	cmd.Stderr = &stderrBuf

	err := cmd.Run()

	res := Result{
		Mode:     ModeShell,
		Program:  shell,
		Args:     cloneStrings(args),
		Command:  req.ShellCommand,
		Stdout:   stdoutBuf.String(),
		Stderr:   stderrBuf.String(),
		ExitCode: ExitCode(err),
	}

	if err != nil {
		return res, fmt.Errorf("shell execution failed: %w", err)
	}

	return res, nil
}

func platformShell() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C"}
	}
	return "sh", []string{"-c"}
}

func hasUnquotedShellMeta(s string) bool {
	inSingle := false
	inDouble := false
	escaped := false

	for _, r := range s {
		if escaped {
			escaped = false
			continue
		}

		switch r {
		case '\\':
			if !inSingle {
				escaped = true
			}
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		default:
			if inSingle || inDouble {
				continue
			}
			switch r {
			case '>', '<', '|', '&', ';', '$', '*', '?', '(', ')', '[', ']', '{', '}', '`', '~':
				return true
			}
		}
	}

	return false
}

func firstToken(s string) string {
	p, _, err := SplitCommandLine(s)
	if err != nil {
		return ""
	}
	return p
}

func isEmpty(req Request) bool {
	return strings.TrimSpace(req.Program) == "" &&
		len(req.Args) == 0 &&
		strings.TrimSpace(req.ShellCommand) == "" &&
		strings.TrimSpace(req.Command) == ""
}

func cloneStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func chooseReason(current, fallback string) string {
	if strings.TrimSpace(current) != "" {
		return current
	}
	return fallback
}

func cloneOptions(opts Options) Options {
	cloned := opts
	if opts.Args != nil {
		cloned.Args = append([]string(nil), opts.Args...)
	}
	if opts.Env != nil {
		cloned.Env = append([]string(nil), opts.Env...)
	}
	return cloned
}

func strconvQuote(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

func joinCommand(program string, args []string) string {
	parts := []string{program}
	for _, a := range args {
		if strings.ContainsAny(a, " \t\n\"'") {
			parts = append(parts, strconvQuote(a))
		} else {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, " ")
}

func detectShell() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C"}
	}
	return "sh", []string{"-c"}
}

func buildCommand(ctx context.Context, opts Options) (*exec.Cmd, Result, error) {
	opts.Mode = normalizeMode(opts.Mode)

	if err := validateOptions(opts); err != nil {
		return nil, Result{}, err
	}

	result := Result{
		Mode:    opts.Mode,
		Program: opts.Program,
		Args:    append([]string(nil), opts.Args...),
		Command: opts.ShellCommand,
	}

	var cmd *exec.Cmd

	switch opts.Mode {
	case ModeArgs:
		cmd = exec.CommandContext(ctx, opts.Program, opts.Args...)
		result.Command = joinCommand(opts.Program, opts.Args)
	case ModeShell:
		shell, shellArgs := detectShell()
		args := append(shellArgs, opts.ShellCommand)
		cmd = exec.CommandContext(ctx, shell, args...)
		result.Shell = shell
		result.Program = shell
		result.Args = append([]string(nil), args...)
		result.Command = opts.ShellCommand
	default:
		return nil, Result{}, fmt.Errorf("%w: %q", ErrInvalidMode, opts.Mode)
	}

	if opts.Dir != "" {
		cmd.Dir = opts.Dir
	}
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}

	return cmd, result, nil
}

func buildOutputWriter(buf *bytes.Buffer, stream io.Writer) io.Writer {
	if stream == nil {
		return buf
	}
	return io.MultiWriter(buf, stream)
}

func Start(ctx context.Context, opts Options) (*Process, error) {
	runCtx, cancel := prepareContext(ctx, opts.Timeout)

	cmd, baseResult, err := buildCommand(runCtx, opts)
	if err != nil {
		cancel()
		return nil, err
	}

	p := &Process{
		cmd:      cmd,
		cancel:   cancel,
		opts:     opts,
		result:   baseResult,
		running:  true,
		waitDone: make(chan struct{}),
		doneCh:   make(chan AsyncResult, 1),
	}

	if err := configureProcessGroup(cmd); err != nil {
		cancel()
		return nil, err
	}

	cmd.Stdout = buildOutputWriter(&p.stdoutBuf, opts.Stdout)
	cmd.Stderr = buildOutputWriter(&p.stderrBuf, opts.Stderr)

	p.startedAt = time.Now()
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}

	if cmd.Process != nil {
		p.result.PID = cmd.Process.Pid
	}

	go func() {
		res, err := p.Wait()
		p.doneCh <- AsyncResult{Result: res, Err: err}
		close(p.doneCh)
	}()

	return p, nil
}

func (p *Process) PID() int {
	if p == nil {
		return 0
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.result.PID
}

func (p *Process) Done() <-chan AsyncResult {
	if p == nil {
		ch := make(chan AsyncResult)
		close(ch)
		return ch
	}
	return p.doneCh
}

func (p *Process) Wait() (Result, error) {
	if p == nil || p.cmd == nil {
		return Result{}, ErrNotStarted
	}

	err := p.cmd.Wait()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.finished {
		return p.result, p.err
	}

	p.running = false
	p.finished = true
	p.err = err
	p.result.Stdout = p.stdoutBuf.String()
	p.result.Stderr = p.stderrBuf.String()
	p.result.ExitCode = ExitCode(err)
	if p.cmd.Process != nil {
		p.result.PID = p.cmd.Process.Pid
	}
	if p.cancel != nil {
		p.cancel()
	}
	close(p.waitDone)

	if err != nil {
		return p.result, fmt.Errorf("process execution failed: %w", err)
	}
	return p.result, nil
}

func (p *Process) IsRunning() bool {
	if p == nil {
		return false
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.running && !p.finished
}

func (p *Process) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return ErrNotStarted
	}
	if !p.IsRunning() {
		return nil
	}
	return killProcessTree(p.cmd.Process.Pid)
}

func (p *Process) GracefulStop(timeout time.Duration) error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return ErrNotStarted
	}
	if !p.IsRunning() {
		return nil
	}

	if err := gracefulStopProcessGroup(p.cmd.Process.Pid); err != nil {
		return err
	}

	if timeout <= 0 {
		return nil
	}

	select {
	case <-p.waitDone:
		return nil
	case <-time.After(timeout):
		return p.Kill()
	}
}
