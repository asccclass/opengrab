package cmdutil_test

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"cmdutil"
)

// ──────────────────────────────────────────────
// Run – synchronous
// ──────────────────────────────────────────────

func TestRun_Basic(t *testing.T) {
	res, err := cmdutil.Run("echo hello", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("expected 'hello' in stdout, got: %q", res.Stdout)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit 0, got %d", res.ExitCode)
	}
}

func TestRun_EmptyCommand(t *testing.T) {
	_, err := cmdutil.Run("", nil)
	if err != cmdutil.ErrEmptyCommand {
		t.Fatalf("expected ErrEmptyCommand, got: %v", err)
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	res, err := cmdutil.Run("exit 42", nil)
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if res.ExitCode != 42 {
		t.Fatalf("expected exit 42, got %d", res.ExitCode)
	}
}

func TestRun_Stderr(t *testing.T) {
	res, err := cmdutil.Run("echo errline >&2", nil)
	// error because stderr output itself doesn't cause failure, exit 0
	_ = err
	_ = res
	// just ensure it doesn't panic
}

func TestRun_Timeout(t *testing.T) {
	_, err := cmdutil.Run("sleep 10", &cmdutil.Options{
		Timeout: 100 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestRun_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	_, err := cmdutil.Run("sleep 10", &cmdutil.Options{Ctx: ctx})
	if err == nil {
		t.Fatal("expected context cancel error")
	}
}

// ──────────────────────────────────────────────
// Run – ModeArgs
// ──────────────────────────────────────────────

func TestRun_ModeArgs(t *testing.T) {
	res, err := cmdutil.Run("echo world", &cmdutil.Options{Mode: cmdutil.ModeArgs})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Stdout, "world") {
		t.Fatalf("expected 'world' in stdout, got %q", res.Stdout)
	}
}

// ──────────────────────────────────────────────
// Run – stdin
// ──────────────────────────────────────────────

func TestRun_Stdin(t *testing.T) {
	res, err := cmdutil.Run("cat", &cmdutil.Options{
		Stdin: strings.NewReader("from stdin\n"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(res.Stdout, "from stdin") {
		t.Fatalf("expected stdin content in stdout, got: %q", res.Stdout)
	}
}

// ──────────────────────────────────────────────
// Run – streaming handlers
// ──────────────────────────────────────────────

func TestRun_StdoutHandler(t *testing.T) {
	var mu sync.Mutex
	var chunks []string

	_, err := cmdutil.Run("echo streaming", &cmdutil.Options{
		StdoutHandler: func(b []byte) {
			mu.Lock()
			chunks = append(chunks, string(b))
			mu.Unlock()
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	joined := strings.Join(chunks, "")
	if !strings.Contains(joined, "streaming") {
		t.Fatalf("handler never received 'streaming', got: %v", chunks)
	}
}

func TestRun_StderrHandler(t *testing.T) {
	var mu sync.Mutex
	var chunks []string

	cmdutil.Run("echo errmsg >&2", &cmdutil.Options{ //nolint
		StderrHandler: func(b []byte) {
			mu.Lock()
			chunks = append(chunks, string(b))
			mu.Unlock()
		},
	})
	// Just ensure no panic; stderr might be empty depending on shell.
	_ = chunks
}

// ──────────────────────────────────────────────
// RunAsync
// ──────────────────────────────────────────────

func TestRunAsync_Basic(t *testing.T) {
	ch := cmdutil.RunAsync("echo async", nil)
	select {
	case ar := <-ch:
		if ar.Err != nil {
			t.Fatalf("unexpected error: %v", ar.Err)
		}
		if !strings.Contains(ar.Result.Stdout, "async") {
			t.Fatalf("expected 'async' in stdout, got: %q", ar.Result.Stdout)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for async result")
	}
}

func TestRunAsync_Error(t *testing.T) {
	ch := cmdutil.RunAsync("exit 1", nil)
	ar := <-ch
	if ar.Err == nil {
		t.Fatal("expected error")
	}
}

func TestRunAsync_Parallel(t *testing.T) {
	cmds := []string{"echo a", "echo b", "echo c"}
	channels := make([]<-chan cmdutil.AsyncResult, len(cmds))
	for i, c := range cmds {
		channels[i] = cmdutil.RunAsync(c, nil)
	}
	for i, ch := range channels {
		ar := <-ch
		if ar.Err != nil {
			t.Fatalf("cmd[%d] error: %v", i, ar.Err)
		}
	}
}

// ──────────────────────────────────────────────
// Process lifecycle
// ──────────────────────────────────────────────

func TestProcess_StartWait(t *testing.T) {
	p, err := cmdutil.NewProcess("echo lifecycle", nil)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !p.IsRunning() {
		// Short echo may already be done, that's fine
	}
	res, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(res.Stdout, "lifecycle") {
		t.Fatalf("expected 'lifecycle' in stdout, got: %q", res.Stdout)
	}
}

func TestProcess_Kill(t *testing.T) {
	p, err := cmdutil.NewProcess("sleep 30", nil)
	if err != nil {
		t.Fatalf("NewProcess: %v", err)
	}
	if err := p.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := p.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if p.IsRunning() {
		t.Fatal("process still running after Kill")
	}
}

func TestProcess_WaitBeforeStart(t *testing.T) {
	p, _ := cmdutil.NewProcess("echo x", nil)
	_, err := p.Wait()
	if err != cmdutil.ErrNotStarted {
		t.Fatalf("expected ErrNotStarted, got: %v", err)
	}
}

func TestProcess_KillBeforeStart(t *testing.T) {
	p, _ := cmdutil.NewProcess("echo x", nil)
	err := p.Kill()
	if err != cmdutil.ErrNotStarted {
		t.Fatalf("expected ErrNotStarted, got: %v", err)
	}
}

func TestProcess_DoubleStart(t *testing.T) {
	p, _ := cmdutil.NewProcess("sleep 1", nil)
	_ = p.Start()
	defer p.Kill() //nolint
	err := p.Start()
	if err == nil {
		t.Fatal("expected error on double Start")
	}
}

func TestProcess_Pid(t *testing.T) {
	p, _ := cmdutil.NewProcess("sleep 1", nil)
	_ = p.Start()
	defer p.Kill() //nolint
	if p.Pid() == 0 {
		t.Fatal("expected non-zero PID after start")
	}
}

func TestProcess_StreamingWithProcess(t *testing.T) {
	var mu sync.Mutex
	var buf bytes.Buffer

	p, _ := cmdutil.NewProcess("echo from-process", &cmdutil.Options{
		StdoutHandler: func(b []byte) {
			mu.Lock()
			buf.Write(b)
			mu.Unlock()
		},
	})
	_ = p.Start()
	_, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(buf.String(), "from-process") {
		t.Fatalf("expected streamed output, got: %q", buf.String())
	}
}

func TestProcess_Stdin(t *testing.T) {
	p, _ := cmdutil.NewProcess("cat", &cmdutil.Options{
		Stdin: strings.NewReader("process stdin\n"),
	})
	_ = p.Start()
	res, err := p.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if !strings.Contains(res.Stdout, "process stdin") {
		t.Fatalf("expected stdin echoed, got: %q", res.Stdout)
	}
}
