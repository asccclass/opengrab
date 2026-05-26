//go:build !windows

package cmdrunner

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

func platformShellConfig() (string, []string) {
	return "sh", []string{"-c"}
}

func configureProcessGroup(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
	}
	return nil
}

func signalProcessGroup(pid int, sig os.Signal) error {
	if pid <= 0 {
		return ErrNotStarted
	}

	syscallSig, ok := sig.(syscall.Signal)
	if !ok {
		if sig == os.Interrupt {
			syscallSig = syscall.SIGINT
		} else {
			return errors.New("unsupported signal type on unix")
		}
	}

	return syscall.Kill(-pid, syscallSig)
}

func gracefulStopProcessGroup(pid int) error {
	return signalProcessGroup(pid, syscall.SIGTERM)
}

func killProcessTree(pid int) error {
	if pid <= 0 {
		return ErrNotStarted
	}

	err := syscall.Kill(-pid, syscall.SIGKILL)
	if err == syscall.ESRCH {
		return nil
	}
	return err
}
