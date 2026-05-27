package cmd

import "testing"

func TestRemoteControlFlagDefaultsToDisabled(t *testing.T) {
	if got := RootCmd.PersistentFlags().Lookup("disable-web-ssh").DefValue; got != "true" {
		t.Fatalf("disable-web-ssh default = %q, want %q", got, "true")
	}
	if got := RootCmd.PersistentFlags().Lookup("enable-remote-control").DefValue; got != "false" {
		t.Fatalf("enable-remote-control default = %q, want %q", got, "false")
	}
	if got := RootCmd.PersistentFlags().Lookup("enable-remote-exec").DefValue; got != "false" {
		t.Fatalf("enable-remote-exec default = %q, want %q", got, "false")
	}
	if got := RootCmd.PersistentFlags().Lookup("enable-terminal").DefValue; got != "false" {
		t.Fatalf("enable-terminal default = %q, want %q", got, "false")
	}
	if got := RootCmd.PersistentFlags().Lookup("enable-ping").DefValue; got != "false" {
		t.Fatalf("enable-ping default = %q, want %q", got, "false")
	}
}

func TestRemoteControlEnabledOnlyWhenExplicitlyRequested(t *testing.T) {
	useGlobalFlagsSnapshot(t)

	flags.DisableWebSsh = true
	flags.EnableRemoteControl = false
	if flags.RemoteControlEnabled() {
		t.Fatal("RemoteControlEnabled() = true, want false when no opt-in is set")
	}

	flags.EnableRemoteControl = true
	if !flags.RemoteControlEnabled() {
		t.Fatal("RemoteControlEnabled() = false, want true when enable-remote-control is set")
	}
}

func TestRemoteCapabilitiesCanBeEnabledIndividually(t *testing.T) {
	useGlobalFlagsSnapshot(t)

	flags.DisableWebSsh = true
	flags.EnableRemoteControl = false
	flags.EnableRemoteExec = false
	flags.EnableTerminal = false
	flags.EnablePing = false

	if flags.RemoteExecEnabled() || flags.TerminalEnabled() || flags.PingEnabled() {
		t.Fatal("expected all remote capabilities to be disabled without explicit opt-in")
	}

	flags.EnableRemoteExec = true
	if !flags.RemoteExecEnabled() || flags.TerminalEnabled() || flags.PingEnabled() {
		t.Fatal("expected only remote exec capability to be enabled")
	}

	flags.EnableRemoteExec = false
	flags.EnableTerminal = true
	if flags.RemoteExecEnabled() || !flags.TerminalEnabled() || flags.PingEnabled() {
		t.Fatal("expected only terminal capability to be enabled")
	}

	flags.EnableTerminal = false
	flags.EnablePing = true
	if flags.RemoteExecEnabled() || flags.TerminalEnabled() || !flags.PingEnabled() {
		t.Fatal("expected only ping capability to be enabled")
	}

	flags.EnableRemoteControl = true
	if !flags.RemoteExecEnabled() || !flags.TerminalEnabled() || !flags.PingEnabled() {
		t.Fatal("expected broad remote control opt-in to enable all remote capabilities")
	}
}