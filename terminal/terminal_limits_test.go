package terminal

import (
	"testing"
	"time"
)

func useTerminalFlagsSnapshot(t *testing.T) {
	t.Helper()

	previous := *flags
	t.Cleanup(func() {
		*flags = previous
	})
}

func resetTerminalSessionLimiter(t *testing.T) {
	t.Helper()

	terminalSessionSlotsMu.Lock()
	originalSlots := terminalSessionSlots
	originalLimit := terminalSessionSlotsLimit
	terminalSessionSlots = nil
	terminalSessionSlotsLimit = 0
	terminalSessionSlotsMu.Unlock()
	t.Cleanup(func() {
		terminalSessionSlotsMu.Lock()
		terminalSessionSlots = originalSlots
		terminalSessionSlotsLimit = originalLimit
		terminalSessionSlotsMu.Unlock()
	})
}

func TestAcquireTerminalSessionSlotRejectsWhenLimitReached(t *testing.T) {
	useTerminalFlagsSnapshot(t)
	resetTerminalSessionLimiter(t)

	flags.MaxTerminalSessions = 1

	releaseFirst, ok := acquireTerminalSessionSlot()
	if !ok {
		t.Fatal("expected first terminal session slot acquisition to succeed")
	}

	if _, ok := acquireTerminalSessionSlot(); ok {
		releaseFirst()
		t.Fatal("expected second terminal session slot acquisition to be rejected")
	}

	releaseFirst()
	if releaseAgain, ok := acquireTerminalSessionSlot(); !ok {
		t.Fatal("expected slot acquisition to succeed after releasing the prior session")
	} else {
		releaseAgain()
	}
}

func TestTerminalSessionDurationsUseSecureDefaults(t *testing.T) {
	useTerminalFlagsSnapshot(t)

	flags.MaxTerminalSessions = 0
	flags.TerminalIdleTimeout = 0
	flags.TerminalMaxDuration = 0

	if got := terminalSessionLimit(); got != defaultMaxTerminalSessions {
		t.Fatalf("terminalSessionLimit() = %d, want %d", got, defaultMaxTerminalSessions)
	}
	if got := terminalIdleTimeout(); got != defaultTerminalIdleTimeout {
		t.Fatalf("terminalIdleTimeout() = %s, want %s", got, defaultTerminalIdleTimeout)
	}
	if got := terminalMaxDuration(); got != defaultTerminalMaxDuration {
		t.Fatalf("terminalMaxDuration() = %s, want %s", got, defaultTerminalMaxDuration)
	}
}

func TestTerminalSessionDurationsRespectConfiguredValues(t *testing.T) {
	useTerminalFlagsSnapshot(t)

	flags.MaxTerminalSessions = 2
	flags.TerminalIdleTimeout = 45
	flags.TerminalMaxDuration = 120

	if got := terminalSessionLimit(); got != 2 {
		t.Fatalf("terminalSessionLimit() = %d, want %d", got, 2)
	}
	if got := terminalIdleTimeout(); got != 45*time.Second {
		t.Fatalf("terminalIdleTimeout() = %s, want %s", got, 45*time.Second)
	}
	if got := terminalMaxDuration(); got != 120*time.Second {
		t.Fatalf("terminalMaxDuration() = %s, want %s", got, 120*time.Second)
	}
}

func TestTerminalCapabilityCanBeEnabledIndividually(t *testing.T) {
	useTerminalFlagsSnapshot(t)

	flags.DisableWebSsh = true
	flags.EnableRemoteControl = false
	flags.EnableTerminal = false
	if terminalCapabilityEnabled() {
		t.Fatal("expected terminal capability to be disabled without explicit opt-in")
	}

	flags.EnableTerminal = true
	if !terminalCapabilityEnabled() {
		t.Fatal("expected terminal capability to be enabled by its dedicated flag")
	}
}