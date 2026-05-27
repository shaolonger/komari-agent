package update

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/blang/semver"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
)

type stubSelfUpdater struct {
	release *selfupdate.Release
	err     error
}

func (s stubSelfUpdater) UpdateSelf(current semver.Version, slug string) (*selfupdate.Release, error) {
	return s.release, s.err
}

func useUpdateHooks(t *testing.T, updater selfUpdater, updateErr error, exitCode *int) {
	t.Helper()

	previousUpdater := newSelfUpdater
	previousExit := exitProcess
	previousVersion := CurrentVersion
	previousClient := http.DefaultClient

	newSelfUpdater = func(config selfupdate.Config) (selfUpdater, error) {
		if updateErr != nil {
			return nil, updateErr
		}
		return updater, nil
	}
	exitProcess = func(code int) {
		*exitCode = code
	}
	CurrentVersion = "1.0.0"

	t.Cleanup(func() {
		newSelfUpdater = previousUpdater
		exitProcess = previousExit
		CurrentVersion = previousVersion
		http.DefaultClient = previousClient
	})
}

// TestParseVersion 验证 parseVersion 能够解析各种版本号格式，包括带 v/V 前缀、预发布和构建元数据
func TestParseVersion(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"v1.2.3", "1.2.3"},
		{"V1.2.3", "1.2.3"},
		{"1.2.3-beta.1", "1.2.3-beta.1"},
		{"v1.2.3+meta", "1.2.3+meta"},
		{"1.2.3-pre.1+build.123", "1.2.3-pre.1+build.123"},
		{"  v2.0.0  ", "2.0.0"},
		{"invalid", ""},
	}

	for _, tt := range tests {
		got, err := parseVersion(strings.TrimSpace(tt.input))
		if tt.want == "" {
			if err == nil {
				t.Errorf("parseVersion(%q) expected error, got %v", tt.input, got)
			}
		} else {
			if err != nil {
				t.Errorf("parseVersion(%q) unexpected error: %v", tt.input, err)
				continue
			}
			if got.String() != tt.want {
				t.Errorf("parseVersion(%q) = %q, want %q", tt.input, got.String(), tt.want)
			}
		}
	}
}

// TestNeedUpdate 验证 needUpdate 在不同版本组合下的判断
func TestNeedUpdate(t *testing.T) {
	tests := []struct {
		current string
		latest  string
		want    bool
	}{
		{"1.0.0", "1.0.1", true},
		{"v1.0.0", "1.1.0", true},
		{"1.2.3", "1.2.3", false},
		{"1.2.4", "1.2.3", false},
		{"1.2.3-beta", "1.2.3", true},
		{"1.2.3", "1.2.3-beta", false},
		{"0.0.5", "0.0.6+build.1", true},
		{"0.0.6", "v0.0.6+build.1", false},
	}

	for _, tt := range tests {
		cur, err := parseVersion(strings.TrimSpace(tt.current))
		if err != nil {
			t.Fatalf("parseVersion(%q) error: %v", tt.current, err)
		}
		lat, err := parseVersion(strings.TrimSpace(tt.latest))
		if err != nil {
			t.Fatalf("parseVersion(%q) error: %v", tt.latest, err)
		}
		got := needUpdate(cur, lat)
		if got != tt.want {
			t.Errorf("needUpdate(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
		}
	}
}

func TestSelfUpdateConfigUsesSHA2Validator(t *testing.T) {
	config := selfUpdateConfig()

	validator, ok := config.Validator.(*selfupdate.SHA2Validator)
	if !ok || validator == nil {
		t.Fatalf("config.Validator = %T, want *selfupdate.SHA2Validator", config.Validator)
	}
	if got := validator.Suffix(); got != ".sha256" {
		t.Fatalf("validator.Suffix() = %q, want %q", got, ".sha256")
	}
}

func TestCheckAndUpdateDoesNotExitOnUpdaterError(t *testing.T) {
	exitCode := 0
	useUpdateHooks(t, stubSelfUpdater{}, nil, &exitCode)

	updateFailure := errors.New("Failed validating asset content: hash mismatch")
	newSelfUpdater = func(config selfupdate.Config) (selfUpdater, error) {
		return stubSelfUpdater{err: updateFailure}, nil
	}

	err := CheckAndUpdate()
	if err == nil {
		t.Fatal("CheckAndUpdate() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "failed to check for updates") {
		t.Fatalf("CheckAndUpdate() error = %q, want substring %q", err.Error(), "failed to check for updates")
	}
	if exitCode != 0 {
		t.Fatalf("exitProcess() called with %d, want not called", exitCode)
	}
}

func TestCheckAndUpdateExitsAfterSuccessfulUpdate(t *testing.T) {
	exitCode := 0
	releaseVersion, err := semver.Parse("1.0.1")
	if err != nil {
		t.Fatalf("semver.Parse() error = %v", err)
	}
	useUpdateHooks(t, stubSelfUpdater{
		release: &selfupdate.Release{Version: releaseVersion},
	}, nil, &exitCode)

	if err := CheckAndUpdate(); err != nil {
		t.Fatalf("CheckAndUpdate() error = %v", err)
	}
	if exitCode != 42 {
		t.Fatalf("exitProcess() code = %d, want %d", exitCode, 42)
	}
}
