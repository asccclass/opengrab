//go:build windows

package cmdrunner

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

var errUnsupportedSignal = errors.New("unsupported signal on windows")

func platformShellConfig() (string, []string) {
	return "cmd", []string{"/C"}
}

func configureProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	return nil
}

func signalProcessGroup(pid int, sig os.Signal) error {
	if pid <= 0 {
		return ErrNotStarted
	}
	if sig != os.Interrupt {
		return fmt.Errorf(
			"%w: windows only supports os.Interrupt via CTRL_BREAK_EVENT here",
			errUnsupportedSignal,
		)
	}
	return sendCtrlBreak(pid)
}

func gracefulStopProcessGroup(pid int) error {
	return sendCtrlBreak(pid)
}

func killProcessTree(pid int) error {
	if pid <= 0 {
		return ErrNotStarted
	}

	cmd := exec.Command("taskkill", "/PID", fmt.Sprintf("%d", pid), "/T", "/F")
	out, err := cmd.CombinedOutput()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			// taskkill 找不到程序時，常見 exit code
			if exitErr.ExitCode() == 128 || exitErr.ExitCode() == 255 {
				return nil
			}
		}
		return fmt.Errorf("taskkill failed: %w: %s", err, string(out))
	}

	return nil
}

func sendCtrlBreak(pid int) error {
	dll, err := syscall.LoadDLL("kernel32.dll")
	if err != nil {
		return err
	}
	defer dll.Release()

	proc, err := dll.FindProc("GenerateConsoleCtrlEvent")
	if err != nil {
		return err
	}

	const ctrlBreakEvent = 1 // CTRL_BREAK_EVENT
	r, _, callErr := proc.Call(uintptr(ctrlBreakEvent), uintptr(pid))
	if r == 0 {
		if callErr != syscall.Errno(0) {
			return callErr
		}
		return errors.New("GenerateConsoleCtrlEvent failed")
	}
	return nil
}
