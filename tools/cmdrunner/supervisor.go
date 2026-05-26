package cmdrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"sync"
	"time"
)

var (
	ErrSupervisorStarted = errors.New("supervisor already started")
	ErrSupervisorStopped = errors.New("supervisor already stopped")
)

type RestartPolicy string

const (
	RestartPolicyNever     RestartPolicy = "never"
	RestartPolicyAlways    RestartPolicy = "always"
	RestartPolicyOnFailure RestartPolicy = "on-failure"
)

type BackoffFunc func(restartCount int) time.Duration
type HealthCheckFunc func(context.Context, *Process) error
type BeforeStartHook func(context.Context, *Supervisor) error
type AfterStopHook func(context.Context, *Supervisor, Result, error) error

type SupervisorOptions struct {
	Process Options

	// 新版建議使用 RestartPolicy
	// 若沒填，會相容舊欄位 AutoRestart
	RestartPolicy RestartPolicy

	// 相容舊版
	AutoRestart bool

	// <= 0 代表 unlimited
	MaxRestarts int

	// Backoff
	Backoff BackoffFunc

	// Hook
	BeforeStart BeforeStartHook
	AfterStop   AfterStopHook

	// Health check
	HealthCheck            HealthCheckFunc
	HealthCheckInterval    time.Duration
	HealthCheckTimeout     time.Duration
	HealthCheckFailures    int
	HealthCheckStopTimeout time.Duration

	// In-memory tail log
	RingBufferBytes int

	// File rotate
	LogRotate *RotateOptions
}

type Metrics struct {
	Running bool

	Starts   uint64
	Stops    uint64
	Restarts uint64
	Crashes  uint64

	HealthChecks          uint64
	HealthCheckSuccesses  uint64
	HealthCheckFailures   uint64
	ConsecutiveRestarts   uint64
	ConsecutiveHCFailures uint64

	CurrentPID int

	LastStartTime time.Time
	LastStopTime  time.Time

	CurrentUptime time.Duration
	TotalUptime   time.Duration

	LastExitCode      int
	LastError         string
	LastHealthError   string
	LastRestartReason string
}

type supervisorMetrics struct {
	starts   uint64
	stops    uint64
	restarts uint64
	crashes  uint64

	healthChecks         uint64
	healthCheckSuccesses uint64
	healthCheckFailures  uint64

	consecutiveRestarts   uint64
	consecutiveHCFailures uint64

	running bool

	currentPID   int
	currentStart time.Time
	totalUptime  time.Duration

	lastStartTime time.Time
	lastStopTime  time.Time

	lastExitCode      int
	lastError         string
	lastHealthError   string
	lastRestartReason string
}

type Supervisor struct {
	opts SupervisorOptions

	parentCtx context.Context
	runCtx    context.Context
	cancel    context.CancelFunc

	mu       sync.RWMutex
	started  bool
	stopping bool
	proc     *Process

	lastResult Result
	lastErr    error
	lastHealth error
	finalErr   error

	// 由 health loop 標記，讓 RestartPolicyOnFailure 能正確重啟
	unhealthyStop bool

	doneCh chan struct{}

	stdoutRing *RingBuffer
	stderrRing *RingBuffer

	stdoutRotate *RotateWriter
	stderrRotate *RotateWriter

	metrics supervisorMetrics
}

func normalizeMode(mode Mode) Mode {
	if mode == "" {
		return ModeArgs
	}
	return mode
}

func validateOptions(opts Options) error {
	switch normalizeMode(opts.Mode) {
	case ModeArgs:
		if strings.TrimSpace(opts.Program) == "" {
			return ErrEmptyProgram
		}
	case ModeShell:
		if strings.TrimSpace(opts.ShellCommand) == "" {
			return ErrEmptyShellCommand
		}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidMode, opts.Mode)
	}
	return nil
}

func NewSupervisor(opts SupervisorOptions) (*Supervisor, error) {
	if err := validateOptions(opts.Process); err != nil {
		return nil, err
	}

	policy, err := normalizeRestartPolicy(opts.RestartPolicy, opts.AutoRestart)
	if err != nil {
		return nil, err
	}
	opts.RestartPolicy = policy

	if opts.RingBufferBytes <= 0 {
		opts.RingBufferBytes = 256 * 1024
	}
	if opts.HealthCheckFailures <= 0 {
		opts.HealthCheckFailures = 1
	}
	if opts.HealthCheckStopTimeout <= 0 {
		opts.HealthCheckStopTimeout = 5 * time.Second
	}

	s := &Supervisor{
		opts:       opts,
		doneCh:     make(chan struct{}),
		stdoutRing: NewRingBuffer(opts.RingBufferBytes),
		stderrRing: NewRingBuffer(opts.RingBufferBytes),
	}

	if opts.LogRotate != nil {
		var err error

		s.stdoutRotate, err = NewRotateWriter(
			opts.LogRotate.StdoutPath,
			opts.LogRotate.MaxBytes,
			opts.LogRotate.MaxBackups,
			opts.LogRotate.FilePerm,
		)
		if err != nil {
			return nil, err
		}

		s.stderrRotate, err = NewRotateWriter(
			opts.LogRotate.StderrPath,
			opts.LogRotate.MaxBytes,
			opts.LogRotate.MaxBackups,
			opts.LogRotate.FilePerm,
		)
		if err != nil {
			_ = s.stdoutRotate.Close()
			return nil, err
		}
	}

	return s, nil
}

func prepareContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	if timeout > 0 {
		return context.WithTimeout(parent, timeout)
	}
	return context.WithCancel(parent)
}

func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.started {
		return ErrSupervisorStarted
	}

	s.parentCtx = ctx
	s.runCtx, s.cancel = prepareContext(ctx, 0)
	s.started = true
	s.stopping = false

	go s.loop()
	return nil
}

func (s *Supervisor) Wait() error {
	if s == nil {
		return nil
	}
	<-s.doneCh

	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.finalErr
}

func (s *Supervisor) Stop(timeout time.Duration) error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return ErrSupervisorStopped
	}
	s.stopping = true
	proc := s.proc
	cancel := s.cancel
	s.mu.Unlock()

	if proc != nil && proc.IsRunning() {
		_ = proc.GracefulStop(timeout)
	}
	if cancel != nil {
		cancel()
	}

	return s.Wait()
}

func (s *Supervisor) CurrentProcess() *Process {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.proc
}

func (s *Supervisor) RestartCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return int(s.metrics.restarts)
}

func (s *Supervisor) LastResult() Result {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastResult
}

func (s *Supervisor) LastError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastErr
}

func (s *Supervisor) LastHealthError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.lastHealth
}

func (s *Supervisor) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.started && !s.stopping && s.proc != nil && s.proc.IsRunning()
}

func (s *Supervisor) StdoutTail() string {
	if s.stdoutRing == nil {
		return ""
	}
	return s.stdoutRing.String()
}

func (s *Supervisor) StderrTail() string {
	if s.stderrRing == nil {
		return ""
	}
	return s.stderrRing.String()
}

func (s *Supervisor) SnapshotMetrics() Metrics {
	s.mu.RLock()
	defer s.mu.RUnlock()

	m := Metrics{
		Running: s.metrics.running,

		Starts:   s.metrics.starts,
		Stops:    s.metrics.stops,
		Restarts: s.metrics.restarts,
		Crashes:  s.metrics.crashes,

		HealthChecks:          s.metrics.healthChecks,
		HealthCheckSuccesses:  s.metrics.healthCheckSuccesses,
		HealthCheckFailures:   s.metrics.healthCheckFailures,
		ConsecutiveRestarts:   s.metrics.consecutiveRestarts,
		ConsecutiveHCFailures: s.metrics.consecutiveHCFailures,

		CurrentPID: s.metrics.currentPID,

		LastStartTime: s.metrics.lastStartTime,
		LastStopTime:  s.metrics.lastStopTime,

		TotalUptime: s.metrics.totalUptime,

		LastExitCode:      s.metrics.lastExitCode,
		LastError:         s.metrics.lastError,
		LastHealthError:   s.metrics.lastHealthError,
		LastRestartReason: s.metrics.lastRestartReason,
	}

	if s.metrics.running && !s.metrics.currentStart.IsZero() {
		m.CurrentUptime = time.Since(s.metrics.currentStart)
		m.TotalUptime += m.CurrentUptime
	}

	return m
}

func (s *Supervisor) loop() {
	defer func() {
		if s.stdoutRotate != nil {
			_ = s.stdoutRotate.Close()
		}
		if s.stderrRotate != nil {
			_ = s.stderrRotate.Close()
		}
		close(s.doneCh)
	}()

	for {
		if s.shouldExit() {
			s.setFinalErr(nil)
			return
		}

		if hook := s.opts.BeforeStart; hook != nil {
			if err := hook(s.runCtx, s); err != nil {
				s.setLast(Result{}, err)
				s.recordCrash(err)
				if !s.shouldRestart("before-start-hook", err, Result{}, false) {
					s.setFinalErr(err)
					return
				}
				s.recordRestart("before-start-hook")
				if !s.sleepBackoff() {
					s.setFinalErr(nil)
					return
				}
				continue
			}
		}

		popts := cloneOptions(s.opts.Process)
		popts.Timeout = 0
		popts.Stdout = combineWriters(popts.Stdout, s.stdoutRing, s.stdoutRotate)
		popts.Stderr = combineWriters(popts.Stderr, s.stderrRing, s.stderrRotate)

		proc, err := Start(s.runCtx, popts)
		if err != nil {
			s.setLast(Result{}, err)
			s.recordCrash(err)
			if !s.shouldRestart("start-failure", err, Result{}, false) {
				s.setFinalErr(err)
				return
			}
			s.recordRestart("start-failure")
			if !s.sleepBackoff() {
				s.setFinalErr(nil)
				return
			}
			continue
		}

		s.setProcess(proc)
		s.recordStart(proc.PID())

		hcStop := make(chan struct{})
		if s.opts.HealthCheck != nil && s.opts.HealthCheckInterval > 0 {
			go s.healthLoop(proc, hcStop)
		}

		done := <-proc.Done()
		close(hcStop)

		unhealthy := s.consumeUnhealthyFlag()

		s.clearProcess()
		s.setLast(done.Result, done.Err)
		s.recordStop(done.Result, done.Err, unhealthy)

		if hook := s.opts.AfterStop; hook != nil {
			if hookErr := hook(context.Background(), s, done.Result, done.Err); hookErr != nil && done.Err == nil {
				done.Err = fmt.Errorf("after stop hook failed: %w", hookErr)
				s.setLast(done.Result, done.Err)
			}
		}

		if s.shouldExit() {
			s.setFinalErr(nil)
			return
		}

		reason := restartReason(done.Result, done.Err, unhealthy)
		if !s.shouldRestart(reason, done.Err, done.Result, unhealthy) {
			s.setFinalErr(terminalError(done.Result, done.Err, s.lastHealth, unhealthy))
			return
		}

		s.recordRestart(reason)

		if !s.sleepBackoff() {
			s.setFinalErr(nil)
			return
		}
	}
}

func (s *Supervisor) healthLoop(proc *Process, stopCh <-chan struct{}) {
	ticker := time.NewTicker(s.opts.HealthCheckInterval)
	defer ticker.Stop()

	failures := 0

	for {
		select {
		case <-stopCh:
			return
		case <-s.runCtx.Done():
			return
		case <-ticker.C:
			if proc == nil || !proc.IsRunning() {
				return
			}

			s.recordHealthCheck()

			hctx := s.runCtx
			cancel := func() {}

			if s.opts.HealthCheckTimeout > 0 {
				hctx, cancel = context.WithTimeout(s.runCtx, s.opts.HealthCheckTimeout)
			}

			err := s.opts.HealthCheck(hctx, proc)
			cancel()

			if err != nil {
				failures++
				s.recordHealthFailure(err)

				if failures >= s.opts.HealthCheckFailures {
					s.markUnhealthy(err)
					_ = proc.GracefulStop(s.opts.HealthCheckStopTimeout)
					return
				}
				continue
			}

			failures = 0
			s.recordHealthSuccess()
		}
	}
}

func (s *Supervisor) shouldExit() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.started {
		return true
	}
	if s.stopping {
		return true
	}
	if s.runCtx != nil && s.runCtx.Err() != nil {
		return true
	}
	return false
}

func (s *Supervisor) shouldRestart(reason string, err error, res Result, unhealthy bool) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.stopping {
		return false
	}
	if s.runCtx != nil && s.runCtx.Err() != nil {
		return false
	}
	if s.opts.MaxRestarts > 0 && int(s.metrics.restarts) >= s.opts.MaxRestarts {
		return false
	}

	switch s.opts.RestartPolicy {
	case RestartPolicyAlways:
		return true

	case RestartPolicyOnFailure:
		if unhealthy {
			return true
		}
		if err != nil {
			return true
		}
		return res.ExitCode != 0

	case RestartPolicyNever:
		return false

	default:
		_ = reason
		return false
	}
}

func (s *Supervisor) sleepBackoff() bool {
	s.mu.RLock()
	restartCount := int(s.metrics.restarts)
	backoff := s.opts.Backoff
	runCtx := s.runCtx
	s.mu.RUnlock()

	if backoff == nil {
		return true
	}

	d := backoff(restartCount)
	if d <= 0 {
		return true
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-runCtx.Done():
		return false
	}
}

func (s *Supervisor) setProcess(proc *Process) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proc = proc
}

func (s *Supervisor) clearProcess() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.proc = nil
}

func (s *Supervisor) setLast(res Result, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastResult = res
	s.lastErr = err
}

func (s *Supervisor) setFinalErr(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalErr = err
}

func (s *Supervisor) markUnhealthy(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unhealthyStop = true
	s.lastHealth = err
	s.metrics.lastHealthError = safeError(err)
}

func (s *Supervisor) consumeUnhealthyFlag() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.unhealthyStop
	s.unhealthyStop = false
	return v
}

func (s *Supervisor) recordStart(pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	s.metrics.starts++
	s.metrics.running = true
	s.metrics.currentPID = pid
	s.metrics.currentStart = now
	s.metrics.lastStartTime = now
}

func (s *Supervisor) recordStop(res Result, err error, unhealthy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()

	s.metrics.stops++
	s.metrics.running = false
	s.metrics.currentPID = 0
	if !s.metrics.currentStart.IsZero() {
		s.metrics.totalUptime += now.Sub(s.metrics.currentStart)
	}
	s.metrics.currentStart = time.Time{}
	s.metrics.lastStopTime = now
	s.metrics.lastExitCode = res.ExitCode
	s.metrics.lastError = safeError(err)

	// 有成功跑完且退出碼為 0，就視為 crash 鏈斷掉
	if err == nil && res.ExitCode == 0 && !unhealthy {
		s.metrics.consecutiveRestarts = 0
		return
	}

	s.metrics.crashes++
}

func (s *Supervisor) recordCrash(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics.crashes++
	s.metrics.lastError = safeError(err)
}

func (s *Supervisor) recordRestart(reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.metrics.restarts++
	s.metrics.consecutiveRestarts++
	s.metrics.lastRestartReason = reason
}

func (s *Supervisor) recordHealthCheck() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.metrics.healthChecks++
}

func (s *Supervisor) recordHealthSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastHealth = nil
	s.metrics.healthCheckSuccesses++
	s.metrics.consecutiveHCFailures = 0
	s.metrics.lastHealthError = ""
}

func (s *Supervisor) recordHealthFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastHealth = err
	s.metrics.healthCheckFailures++
	s.metrics.consecutiveHCFailures++
	s.metrics.lastHealthError = safeError(err)
}

func combineWriters(writers ...io.Writer) io.Writer {
	var filtered []io.Writer
	for _, w := range writers {
		if w != nil {
			filtered = append(filtered, w)
		}
	}

	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return io.MultiWriter(filtered...)
	}
}

func normalizeRestartPolicy(policy RestartPolicy, autoRestart bool) (RestartPolicy, error) {
	switch policy {
	case "":
		if autoRestart {
			return RestartPolicyAlways, nil
		}
		return RestartPolicyNever, nil
	case RestartPolicyNever, RestartPolicyAlways, RestartPolicyOnFailure:
		return policy, nil
	default:
		return "", fmt.Errorf("invalid restart policy: %q", policy)
	}
}

func restartReason(res Result, err error, unhealthy bool) string {
	switch {
	case unhealthy:
		return "health-check-failed"
	case err != nil:
		return "process-error"
	case res.ExitCode != 0:
		return "non-zero-exit"
	default:
		return "policy"
	}
}

func terminalError(res Result, err error, healthErr error, unhealthy bool) error {
	if unhealthy && healthErr != nil {
		return fmt.Errorf("process stopped by health check: %w", healthErr)
	}
	if err != nil {
		return err
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("process exited with code %d", res.ExitCode)
	}
	return nil
}

func safeError(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func FixedBackoff(d time.Duration) BackoffFunc {
	return func(_ int) time.Duration {
		return d
	}
}

func ExponentialBackoff(base, max time.Duration) BackoffFunc {
	if base <= 0 {
		base = 500 * time.Millisecond
	}
	if max <= 0 {
		max = 30 * time.Second
	}

	return func(restartCount int) time.Duration {
		d := base
		for i := 1; i < restartCount; i++ {
			d *= 2
			if d >= max {
				return max
			}
		}
		if d > max {
			return max
		}
		return d
	}
}

// JitterBackoff 會在 base backoff 上加抖動，避免多個實例同時重啟。
// ratio 建議 0.1 ~ 0.3，例如 0.2 代表 ±20%。
func JitterBackoff(base BackoffFunc, ratio float64) BackoffFunc {
	if base == nil {
		return nil
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}

	return func(restartCount int) time.Duration {
		d := base(restartCount)
		if d <= 0 || ratio == 0 {
			return d
		}

		f := 1 - ratio + rand.Float64()*(2*ratio)
		if f < 0 {
			f = 0
		}
		return time.Duration(float64(d) * f)
	}
}
