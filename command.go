package main

import (
	"context"
	"fmt"

	"github.com/asccclass/opengrab/tools/cmdrunner"
)

func runCommand(command string) string {
	result, err := cmdrunner.Run(context.Background(), cmdrunner.Request{
		Mode:    cmdrunner.ModeAuto,
		Command: command,
	})
	if err != nil {
		return fmt.Sprintf("command error: %v\nstdout: %s\nstderr: %s", err, result.Stdout, result.Stderr)
	}
	return result.Stdout
}
