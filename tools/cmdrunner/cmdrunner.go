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
	"time"

	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

type StreamHandler func([]byte)

type Options struct {
	Command string
	Dir     string
	Env     []string
	Timeout time.Duration

	// 支援 stdin 傳入
	Stdin io.Reader

	// 支援即時串流 stdout / stderr
	StdoutHandler StreamHandler
	StderrHandler StreamHandler
}

type Result struct {
	Command   string
	Shell     string
	Stdout    string
	Stderr    string
	ExitCode  int
	StartTime time.Time
	EndTime   time.Time
	Duration  time.Duration
}

type AsyncResult struct {
	Result Result
	Err    error
}

type AsyncHandle struct {
	ResultChan <-chan AsyncResult
	Cancel     context.CancelFunc
}

func detectShell() (string, []string) {
	if runtime.GOOS == "windows" {
		return "cmd", []string{"/C"}
	}
	return "sh", []string{"-c"}
}

type callbackWriter struct {
	fn StreamHandler
}

// DecodeBig5 將 Big5 編碼的 byte 陣列轉換為 UTF-8 字串
func DecodeBig5(s []byte) string {
	reader := transform.NewReader(bytes.NewReader(s), traditionalchinese.Big5.NewDecoder())
	decoded, err := io.ReadAll(reader)
	if err != nil {
		return string(s)
	}
	return string(decoded)
}

func (w callbackWriter) Write(p []byte) (int, error) {
	if w.fn != nil {
		cp := append([]byte(nil), p...)
		w.fn(cp)
	}
	return len(p), nil
}

func exitCode(cmd *exec.Cmd, err error) int {
	if cmd != nil && cmd.ProcessState != nil {
		return cmd.ProcessState.ExitCode()
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}

	return -1
}

func wrapExecError(ctx context.Context, err error, result Result) error {
	if err == nil {
		return nil
	}

	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("command timeout: %w", ctx.Err())
	}

	if errors.Is(ctx.Err(), context.Canceled) {
		return fmt.Errorf("command canceled: %w", ctx.Err())
	}

	return fmt.Errorf(
		"command execution failed: %w (exitCode=%d, stderr=%q)",
		err,
		result.ExitCode,
		result.Stderr,
	)
}

func runWithContext(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if opts.Command == "" {
		return Result{}, errors.New("command is empty")
	}

	shell, shellArgs := detectShell()
	args := append(shellArgs, opts.Command)

	cmd := exec.CommandContext(ctx, shell, args...)
	cmd.Dir = opts.Dir
	if len(opts.Env) > 0 {
		cmd.Env = append(os.Environ(), opts.Env...)
	}

	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}

	var stdoutBuf bytes.Buffer
	var stderrBuf bytes.Buffer

	stdoutWriter := io.Writer(&stdoutBuf)
	stderrWriter := io.Writer(&stderrBuf)

	if opts.StdoutHandler != nil {
		stdoutWriter = io.MultiWriter(&stdoutBuf, callbackWriter{fn: opts.StdoutHandler})
	}
	if opts.StderrHandler != nil {
		stderrWriter = io.MultiWriter(&stderrBuf, callbackWriter{fn: opts.StderrHandler})
	}

	cmd.Stdout = stdoutWriter
	cmd.Stderr = stderrWriter

	start := time.Now()
	err := cmd.Run()
	end := time.Now()

	stdout := stdoutBuf.String()
	stderr := stderrBuf.String()
	if runtime.GOOS == "windows" {
		stdout = DecodeBig5(stdoutBuf.Bytes())
		stderr = DecodeBig5(stderrBuf.Bytes())
	}

	result := Result{
		Command:   opts.Command,
		Shell:     shell,
		Stdout:    stdout,
		Stderr:    stderr,
		ExitCode:  exitCode(cmd, err),
		StartTime: start,
		EndTime:   end,
		Duration:  end.Sub(start),
	}

	return result, wrapExecError(ctx, err, result)
}

// 同步執行
func Run(ctx context.Context, opts Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}

	return runWithContext(ctx, opts)
}

// 非同步執行
func RunAsync(ctx context.Context, opts Options) *AsyncHandle {
	if ctx == nil {
		ctx = context.Background()
	}

	var runCtx context.Context
	var cancel context.CancelFunc

	if opts.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, opts.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}

	ch := make(chan AsyncResult, 1)

	go func() {
		defer close(ch)
		result, err := runWithContext(runCtx, opts)
		ch <- AsyncResult{
			Result: result,
			Err:    err,
		}
	}()

	return &AsyncHandle{
		ResultChan: ch,
		Cancel:     cancel,
	}
}
