package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func useGlobalFlagsSnapshot(t *testing.T) {
	t.Helper()

	previous := *flags
	t.Cleanup(func() {
		*flags = previous
	})
}

func TestLoadTokenFromFileLoadsTokenWhenMissing(t *testing.T) {
	useGlobalFlagsSnapshot(t)

	baseDir := t.TempDir()
	tokenPath := filepath.Join(baseDir, "agent.token")
	if err := os.WriteFile(tokenPath, []byte(" test-token\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	flags.Token = ""
	flags.TokenFile = tokenPath

	if err := loadTokenFromFile(); err != nil {
		t.Fatalf("loadTokenFromFile() error = %v", err)
	}
	if flags.Token != "test-token" {
		t.Fatalf("flags.Token = %q, want %q", flags.Token, "test-token")
	}
}

func TestLoadTokenFromFileDoesNotOverrideExistingToken(t *testing.T) {
	useGlobalFlagsSnapshot(t)

	baseDir := t.TempDir()
	tokenPath := filepath.Join(baseDir, "agent.token")
	if err := os.WriteFile(tokenPath, []byte("file-token\n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	flags.Token = "flag-token"
	flags.TokenFile = tokenPath

	if err := loadTokenFromFile(); err != nil {
		t.Fatalf("loadTokenFromFile() error = %v", err)
	}
	if flags.Token != "flag-token" {
		t.Fatalf("flags.Token = %q, want %q", flags.Token, "flag-token")
	}
}

func TestLoadTokenFromFileRejectsEmptyFile(t *testing.T) {
	useGlobalFlagsSnapshot(t)

	baseDir := t.TempDir()
	tokenPath := filepath.Join(baseDir, "agent.token")
	if err := os.WriteFile(tokenPath, []byte(" \n"), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	flags.Token = ""
	flags.TokenFile = tokenPath

	if err := loadTokenFromFile(); err == nil {
		t.Fatal("loadTokenFromFile() error = nil, want non-nil")
	}
}
