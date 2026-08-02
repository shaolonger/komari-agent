package update

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/komari-monitor/komari-agent/dnsresolver"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
)

var (
	CurrentVersion string = "v1.4.0"
	Repo           string = "shaolonger/komari-agent"
	BuildCommit    string = "unknown"
)

const RestartExitCode = 42

var ErrUpdateInstalled = errors.New("agent update installed; restart required")

const (
	updateStageVersionParse = "current version parsing"
	updateStageUpdaterInit  = "updater initialization"
	updateStageExecution    = "update retrieval, verification, or installation"
)

type selfUpdater interface {
	UpdateSelf(current semver.Version, slug string) (*selfupdate.Release, error)
}

var newSelfUpdater = func(ctx context.Context, config selfupdate.Config, client *http.Client) (selfUpdater, error) {
	return newVerifiedSelfUpdater(ctx, config, client)
}

func failUpdate(stage string) error {
	log.Printf("Auto-update failed during %s; keeping current version", stage)
	return fmt.Errorf("auto-update failed during %s", stage)
}

// parseVersion 解析可能带有 v/V 前缀，以及预发布或构建元数据的版本字符串
func parseVersion(ver string) (semver.Version, error) {
	ver = strings.TrimPrefix(ver, "v")
	ver = strings.TrimPrefix(ver, "V")
	return semver.ParseTolerant(ver)
}

// needUpdate 判断是否需要更新
func needUpdate(current, latest semver.Version) bool {
	// 返回最新版本大于当前版本时需要更新
	return latest.Compare(current) > 0
}

func DoUpdateWorks() {
	_ = DoUpdateWorksContext(context.Background())
}

func DoUpdateWorksContext(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("update worker requires a parent context")
	}
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := CheckAndUpdateContext(ctx); err != nil && ctx.Err() == nil {
				if errors.Is(err, ErrUpdateInstalled) {
					return err
				}
				log.Printf("Periodic auto-update failed: %v", err)
			}
		}
	}
}

func selfUpdateConfig() selfupdate.Config {
	return selfupdate.Config{
		Validator: &selfupdate.SHA2Validator{},
	}
}

// 检查更新并执行自动更新
func CheckAndUpdate() error {
	return CheckAndUpdateContext(context.Background())
}

func CheckAndUpdateContext(ctx context.Context) error {
	if ctx == nil {
		return failUpdate(updateStageUpdaterInit)
	}
	log.Println("Checking update...")
	// Parse current version
	currentSemVer, err := parseVersion(CurrentVersion)
	if err != nil {
		return failUpdate(updateStageVersionParse)
	}

	config := selfUpdateConfig()
	updateContext, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	updater, err := newSelfUpdater(updateContext, config, dnsresolver.GetUpdateHTTPClient())
	if err != nil {
		return failUpdate(updateStageUpdaterInit)
	}

	// Check for latest version
	latest, err := updater.UpdateSelf(currentSemVer, Repo)
	if err != nil {
		return failUpdate(updateStageExecution)
	}

	// Determine if update is needed
	if latest.Version.Equals(currentSemVer) {
		log.Println("Current version is the latest:", CurrentVersion)
		return nil
	}
	// Default is installed as a service, so don't automatically restart
	//execPath, err := os.Executable()
	//if err != nil {
	//	return fmt.Errorf("failed to get current executable path: %v", err)
	//}

	// _, err = os.StartProcess(execPath, os.Args, &os.ProcAttr{
	// 	Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	// })
	// if err != nil {
	// 	return fmt.Errorf("failed to restart program: %v", err)
	// }
	log.Printf("Successfully updated to version %s\n", latest.Version)
	return ErrUpdateInstalled
}
