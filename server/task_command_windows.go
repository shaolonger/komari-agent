//go:build windows

package server

import (
	"context"
	"os/exec"
	"time"
)

func newTaskCommand(ctx context.Context, command string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "powershell", "-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", "[Console]::OutputEncoding = [System.Text.Encoding]::UTF8; "+command)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}