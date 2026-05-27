package cmd

import "testing"

func TestRemoteControlFlagDefaultsToDisabled(t *testing.T) {
	if got := RootCmd.PersistentFlags().Lookup("disable-web-ssh").DefValue; got != "true" {
		t.Fatalf("disable-web-ssh default = %q, want %q", got, "true")
	}
	if got := RootCmd.PersistentFlags().Lookup("enable-remote-control").DefValue; got != "false" {
		t.Fatalf("enable-remote-control default = %q, want %q", got, "false")
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