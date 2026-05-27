package update

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/komari-monitor/komari-agent/dnsresolver"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
)

var (
	CurrentVersion string = "0.0.1"
	Repo           string = "komari-monitor/komari-agent"
)

const (
	updateStageVersionParse = "current version parsing"
	updateStageUpdaterInit  = "updater initialization"
	updateStageExecution    = "update retrieval, verification, or installation"
)

type selfUpdater interface {
	UpdateSelf(current semver.Version, slug string) (*selfupdate.Release, error)
}

var newSelfUpdater = func(config selfupdate.Config) (selfUpdater, error) {
	return selfupdate.NewUpdater(config)
}

var exitProcess = os.Exit

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
	ticker_ := time.NewTicker(time.Duration(6) * time.Hour)
	for range ticker_.C {
		CheckAndUpdate()
	}
}

func selfUpdateConfig() selfupdate.Config {
	return selfupdate.Config{
		Validator: &selfupdate.SHA2Validator{},
	}
}

// 检查更新并执行自动更新
func CheckAndUpdate() error {
	log.Println("Checking update...")
	// Parse current version
	currentSemVer, err := parseVersion(CurrentVersion)
	if err != nil {
		return failUpdate(updateStageVersionParse)
	}

	http.DefaultClient = dnsresolver.GetHTTPClient(60 * time.Second)
	config := selfUpdateConfig()
	updater, err := newSelfUpdater(config)
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
	exitProcess(42)
	return nil
}
