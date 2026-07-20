//go:build !windows

package terminal

import (
	"bytes"
	"syscall"
	"testing"
	"time"
)

func TestNewTerminalImplKeepsInteractiveShellAlive(t *testing.T) {
	impl, err := newTerminalImpl()
	if err != nil {
		t.Fatalf("newTerminalImpl() error = %v", err)
	}

	unixTerm, ok := impl.term.(*unixTerminal)
	if !ok {
		t.Fatalf("terminal type = %T, want *unixTerminal", impl.term)
	}
	t.Cleanup(func() {
		_ = unixTerm.tty.Close()
		_ = unixTerm.cmd.Process.Kill()
		_, _ = unixTerm.cmd.Process.Wait()
	})
	time.Sleep(250 * time.Millisecond)
	if err := syscall.Kill(unixTerm.cmd.Process.Pid, 0); err != nil {
		t.Fatalf("interactive shell exited immediately: %v", err)
	}

	const marker = "KOMARI_TERMINAL_READY"
	if _, err := impl.term.Write([]byte("printf 'KOMARI_%s_READY\\n' TERMINAL\n")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	result := make(chan error, 1)
	go func() {
		buffer := make([]byte, 4096)
		var output bytes.Buffer
		for {
			n, readErr := impl.term.Read(buffer)
			if n > 0 {
				output.Write(buffer[:n])
				if bytes.Contains(output.Bytes(), []byte(marker)) {
					result <- nil
					return
				}
			}
			if readErr != nil {
				result <- readErr
				return
			}
		}
	}()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Read() error before marker = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for interactive shell output")
	}

	time.Sleep(250 * time.Millisecond)
	if _, err := syscall.Getpgid(unixTerm.cmd.Process.Pid); err != nil {
		t.Fatalf("interactive shell exited after executing a command: %v", err)
	}
}
