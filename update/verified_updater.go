package update

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/blang/semver"
	binaryupdate "github.com/inconshreveable/go-update"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
)

const (
	defaultGitHubAPIBase   = "https://api.github.com"
	maximumReleaseMetadata = 8 * 1024 * 1024
	maximumReleaseAsset    = 128 * 1024 * 1024
	maximumValidationAsset = 64 * 1024
	updateAPIVersion       = "2022-11-28"
	updateUserAgent        = "komari-agent-updater"
)

var repositoryComponentPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type verifiedSelfUpdater struct {
	ctx         context.Context
	client      *http.Client
	validator   selfupdate.Validator
	apiBase     string
	goos        string
	goarch      string
	githubToken string
	install     func([]byte, string) error
}

type githubReleaseDocument struct {
	TagName     string                `json:"tag_name"`
	Name        string                `json:"name"`
	HTMLURL     string                `json:"html_url"`
	Body        string                `json:"body"`
	Draft       bool                  `json:"draft"`
	Prerelease  bool                  `json:"prerelease"`
	PublishedAt time.Time             `json:"published_at"`
	Assets      []githubAssetDocument `json:"assets"`
}

type githubAssetDocument struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Size               int    `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type selectedUpdate struct {
	release    *selfupdate.Release
	asset      githubAssetDocument
	validation githubAssetDocument
}

func newVerifiedSelfUpdater(
	ctx context.Context,
	config selfupdate.Config,
	client *http.Client,
) (*verifiedSelfUpdater, error) {
	if ctx == nil {
		return nil, errors.New("update requires a context")
	}
	if client == nil || client.Transport == nil {
		return nil, errors.New("update requires an explicit HTTP client")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		return nil, errors.New("update requires an auditable HTTP transport")
	}
	if transport.TLSClientConfig != nil && transport.TLSClientConfig.InsecureSkipVerify {
		return nil, errors.New("update transport must verify TLS certificates")
	}
	updater := &verifiedSelfUpdater{
		ctx:         ctx,
		client:      client,
		validator:   config.Validator,
		apiBase:     defaultGitHubAPIBase,
		goos:        runtime.GOOS,
		goarch:      runtime.GOARCH,
		githubToken: strings.TrimSpace(os.Getenv("GITHUB_TOKEN")),
	}
	updater.install = updater.installCurrentExecutable
	return updater, nil
}

func (updater *verifiedSelfUpdater) UpdateSelf(current semver.Version, slug string) (*selfupdate.Release, error) {
	selected, err := updater.detectLatest(slug)
	if err != nil {
		return nil, err
	}
	if selected == nil || !selected.release.Version.GT(current) {
		return &selfupdate.Release{Version: current}, nil
	}

	asset, err := updater.download(selected.asset.BrowserDownloadURL, selected.asset.Size, maximumReleaseAsset)
	if err != nil {
		return nil, fmt.Errorf("download release asset: %w", err)
	}
	validation, err := updater.download(
		selected.validation.BrowserDownloadURL,
		selected.validation.Size,
		maximumValidationAsset,
	)
	if err != nil {
		return nil, fmt.Errorf("download validation asset: %w", err)
	}
	if err := validateDownloadedAsset(updater.validator, asset, validation); err != nil {
		return nil, fmt.Errorf("validate release asset: %w", err)
	}
	if err := updater.install(asset, selected.asset.BrowserDownloadURL); err != nil {
		return nil, fmt.Errorf("install release asset: %w", err)
	}
	return selected.release, nil
}

func (updater *verifiedSelfUpdater) detectLatest(slug string) (*selectedUpdate, error) {
	owner, repository, err := parseRepositorySlug(slug)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(updater.apiBase, "/") + "/repos/" +
		url.PathEscape(owner) + "/" + url.PathEscape(repository) + "/releases?per_page=100"
	req, err := http.NewRequestWithContext(updater.ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", updateUserAgent)
	req.Header.Set("X-GitHub-Api-Version", updateAPIVersion)
	if updater.githubToken != "" && strings.EqualFold(req.URL.Hostname(), "api.github.com") {
		req.Header.Set("Authorization", "Bearer "+updater.githubToken)
	}

	resp, err := updater.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL.Scheme != "https" {
		return nil, errors.New("GitHub releases API redirected to a non-HTTPS endpoint")
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("GitHub releases API returned status %d", resp.StatusCode)
	}
	metadata, err := readBounded(resp.Body, maximumReleaseMetadata)
	if err != nil {
		return nil, fmt.Errorf("read GitHub release metadata: %w", err)
	}
	var releases []githubReleaseDocument
	if err := json.Unmarshal(metadata, &releases); err != nil {
		return nil, fmt.Errorf("decode GitHub release metadata: %w", err)
	}
	return selectLatestUpdate(releases, owner, repository, updater.goos, updater.goarch, updater.validator)
}

func (updater *verifiedSelfUpdater) download(endpoint string, declaredSize, maximumSize int) ([]byte, error) {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" {
		return nil, errors.New("update asset URL must use HTTPS")
	}
	if declaredSize < 0 || declaredSize > maximumSize {
		return nil, fmt.Errorf("declared asset size %d exceeds limit %d", declaredSize, maximumSize)
	}
	req, err := http.NewRequestWithContext(updater.ctx, http.MethodGet, parsed.String(), http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("User-Agent", updateUserAgent)
	resp, err := updater.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Request == nil || resp.Request.URL.Scheme != "https" {
		return nil, errors.New("update asset redirected to a non-HTTPS endpoint")
	}
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
		return nil, fmt.Errorf("asset server returned status %d", resp.StatusCode)
	}
	if resp.ContentLength > int64(maximumSize) {
		return nil, fmt.Errorf("asset response length %d exceeds limit %d", resp.ContentLength, maximumSize)
	}
	return readBounded(resp.Body, maximumSize)
}

func (updater *verifiedSelfUpdater) installCurrentExecutable(asset []byte, assetURL string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err == nil {
		executable = resolved
	}
	command, err := selfupdate.UncompressCommand(bytes.NewReader(asset), assetURL, filepath.Base(executable))
	if err != nil {
		return err
	}
	return binaryupdate.Apply(command, binaryupdate.Options{TargetPath: executable})
}

func parseRepositorySlug(slug string) (string, string, error) {
	parts := strings.Split(slug, "/")
	if len(parts) != 2 || !repositoryComponentPattern.MatchString(parts[0]) || !repositoryComponentPattern.MatchString(parts[1]) {
		return "", "", errors.New("invalid update repository slug")
	}
	return parts[0], parts[1], nil
}

func selectLatestUpdate(
	releases []githubReleaseDocument,
	owner string,
	repository string,
	goos string,
	goarch string,
	validator selfupdate.Validator,
) (*selectedUpdate, error) {
	if validator == nil {
		return nil, errors.New("release validation is required")
	}
	var best *selectedUpdate
	for _, candidate := range releases {
		if candidate.Draft || candidate.Prerelease {
			continue
		}
		version, err := parseVersion(strings.TrimSpace(candidate.TagName))
		if err != nil {
			continue
		}
		asset, ok := findPlatformAsset(candidate.Assets, goos, goarch)
		if !ok {
			continue
		}
		if best != nil && !version.GT(best.release.Version) {
			continue
		}
		validationName := asset.Name + validator.Suffix()
		validation, _ := findNamedAsset(candidate.Assets, validationName)
		publishedAt := candidate.PublishedAt
		best = &selectedUpdate{
			release: &selfupdate.Release{
				Version:           version,
				AssetURL:          asset.BrowserDownloadURL,
				AssetByteSize:     asset.Size,
				AssetID:           asset.ID,
				ValidationAssetID: validation.ID,
				URL:               candidate.HTMLURL,
				ReleaseNotes:      candidate.Body,
				Name:              candidate.Name,
				PublishedAt:       &publishedAt,
				RepoOwner:         owner,
				RepoName:          repository,
			},
			asset:      asset,
			validation: validation,
		}
	}
	if best != nil && best.validation.Name == "" {
		return nil, fmt.Errorf("release %s is missing validation asset %q", best.release.Version, best.asset.Name+validator.Suffix())
	}
	return best, nil
}

func findPlatformAsset(assets []githubAssetDocument, goos, goarch string) (githubAssetDocument, bool) {
	for _, asset := range assets {
		for _, suffix := range platformAssetSuffixes(goos, goarch) {
			if strings.HasSuffix(asset.Name, suffix) {
				return asset, true
			}
		}
	}
	return githubAssetDocument{}, false
}

func platformAssetSuffixes(goos, goarch string) []string {
	extensions := []string{".zip", ".tar.gz", ".tgz", ".gzip", ".gz", ".tar.xz", ".xz", ""}
	suffixes := make([]string, 0, len(extensions)*4)
	for _, separator := range []string{"_", "-"} {
		base := goos + separator + goarch
		for _, extension := range extensions {
			suffixes = append(suffixes, base+extension)
			if goos == "windows" {
				suffixes = append(suffixes, base+".exe"+extension)
			}
		}
	}
	return suffixes
}

func findNamedAsset(assets []githubAssetDocument, name string) (githubAssetDocument, bool) {
	for _, asset := range assets {
		if asset.Name == name {
			return asset, true
		}
	}
	return githubAssetDocument{}, false
}

func validateDownloadedAsset(validator selfupdate.Validator, release, validation []byte) error {
	if validator == nil {
		return errors.New("release validation is required")
	}
	if validator.Suffix() == ".sha256" {
		fields := bytes.Fields(validation)
		if len(fields) == 0 || len(fields[0]) != sha256.Size*2 {
			return errors.New("invalid SHA-256 validation asset")
		}
		expected, err := hex.DecodeString(string(fields[0]))
		if err != nil {
			return errors.New("invalid SHA-256 validation asset")
		}
		actual := sha256.Sum256(release)
		if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
			return errors.New("SHA-256 checksum mismatch")
		}
		return nil
	}
	if len(validation) == 0 {
		return errors.New("empty release validation asset")
	}
	return validator.Validate(release, validation)
}

func readBounded(reader io.Reader, maximum int) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, int64(maximum)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maximum {
		return nil, fmt.Errorf("response exceeds %d-byte limit", maximum)
	}
	return data, nil
}
