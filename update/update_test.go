package update

import (
	"bytes"
	"errors"
	"log"
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

func captureUpdateLogs(t *testing.T) *bytes.Buffer {
	t.Helper()

	var buf bytes.Buffer
	previousWriter := log.Writer()
	previousFlags := log.Flags()

	log.SetOutput(&buf)
	log.SetFlags(0)

	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})

	return &buf
}

func assertSanitizedUpdateFailure(t *testing.T, err error, logs string, stage string, sensitive string) {
	t.Helper()

	wantError := "auto-update failed during " + stage
	if err == nil {
		t.Fatal("CheckAndUpdate() error = nil, want non-nil")
	}
	if err.Error() != wantError {
		t.Fatalf("CheckAndUpdate() error = %q, want %q", err.Error(), wantError)
	}
	if strings.Contains(err.Error(), sensitive) {
		t.Fatalf("CheckAndUpdate() error = %q, should not contain sensitive substring %q", err.Error(), sensitive)
	}

	wantLog := "Auto-update failed during " + stage + "; keeping current version"
	if !strings.Contains(logs, wantLog) {
		t.Fatalf("log output = %q, want substring %q", logs, wantLog)
	}
	if strings.Contains(logs, sensitive) {
		t.Fatalf("log output = %q, should not contain sensitive substring %q", logs, sensitive)
	}
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

func TestCheckAndUpdateSanitizesVersionParseFailure(t *testing.T) {
	exitCode := 0
	useUpdateHooks(t, stubSelfUpdater{}, nil, &exitCode)
	CurrentVersion = "invalid-version https://updates.example/?token=secret"
	logs := captureUpdateLogs(t)

	err := CheckAndUpdate()
	assertSanitizedUpdateFailure(t, err, logs.String(), updateStageVersionParse, "token=secret")
	if exitCode != 0 {
		t.Fatalf("exitProcess() called with %d, want not called", exitCode)
	}
}

func TestCheckAndUpdateSanitizesUpdaterCreationError(t *testing.T) {
	exitCode := 0
	sensitive := "https://updates.example/download?token=secret"
	useUpdateHooks(t, stubSelfUpdater{}, errors.New(sensitive), &exitCode)
	logs := captureUpdateLogs(t)

	err := CheckAndUpdate()
	assertSanitizedUpdateFailure(t, err, logs.String(), updateStageUpdaterInit, sensitive)
	if exitCode != 0 {
		t.Fatalf("exitProcess() called with %d, want not called", exitCode)
	}
}

func TestCheckAndUpdateDoesNotExitOnUpdaterError(t *testing.T) {
	exitCode := 0
	useUpdateHooks(t, stubSelfUpdater{}, nil, &exitCode)
	logs := captureUpdateLogs(t)

	updateFailure := errors.New("Failed validating asset content for https://updates.example/download?token=secret")
	newSelfUpdater = func(config selfupdate.Config) (selfUpdater, error) {
		return stubSelfUpdater{err: updateFailure}, nil
	}

	err := CheckAndUpdate()
	assertSanitizedUpdateFailure(t, err, logs.String(), updateStageExecution, "token=secret")
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
