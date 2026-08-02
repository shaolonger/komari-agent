package update

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blang/semver"
	"github.com/rhysd/go-github-selfupdate/selfupdate"
)

func trustedClientForTLSServer(server *httptest.Server) *http.Client {
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    roots,
	}
	return &http.Client{Transport: transport}
}

func TestVerifiedSelfUpdaterDownloadsValidatesAndInstallsWithExplicitClient(t *testing.T) {
	asset := []byte("fixture-agent-binary")
	assetName := "komari-agent-" + runtime.GOOS + "-" + runtime.GOARCH
	checksum := fmt.Sprintf("%x  %s\n", sha256.Sum256(asset), assetName)
	var requests atomic.Int32
	var baseURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		switch request.URL.Path {
		case "/repos/owner/repository/releases":
			_ = json.NewEncoder(writer).Encode([]map[string]interface{}{{
				"tag_name":     "v1.1.0",
				"name":         "fixture release",
				"html_url":     baseURL + "/release",
				"body":         "notes",
				"published_at": time.Now().UTC(),
				"assets": []map[string]interface{}{
					{"id": 1, "name": assetName, "size": len(asset), "browser_download_url": baseURL + "/asset"},
					{"id": 2, "name": assetName + ".sha256", "size": len(checksum), "browser_download_url": baseURL + "/checksum"},
				},
			}})
		case "/asset":
			_, _ = writer.Write(asset)
		case "/checksum":
			_, _ = writer.Write([]byte(checksum))
		default:
			http.NotFound(writer, request)
		}
	}))
	baseURL = server.URL
	defer server.Close()

	updater, err := newVerifiedSelfUpdater(context.Background(), selfUpdateConfig(), trustedClientForTLSServer(server))
	if err != nil {
		t.Fatal(err)
	}
	updater.apiBase = baseURL
	updater.githubToken = ""
	var installed []byte
	updater.install = func(data []byte, endpoint string) error {
		installed = append([]byte(nil), data...)
		if endpoint != baseURL+"/asset" {
			t.Fatalf("install endpoint = %q", endpoint)
		}
		return nil
	}

	release, err := updater.UpdateSelf(semver.MustParse("1.0.0"), "owner/repository")
	if err != nil {
		t.Fatal(err)
	}
	if release.Version.String() != "1.1.0" || string(installed) != string(asset) {
		t.Fatalf("release = %+v installed = %q", release, installed)
	}
	if got := requests.Load(); got != 3 {
		t.Fatalf("requests = %d, want metadata + binary + checksum", got)
	}
}

func TestVerifiedSelfUpdaterRejectsChecksumMismatchWithoutInstall(t *testing.T) {
	asset := []byte("fixture-agent-binary")
	assetName := "komari-agent-" + runtime.GOOS + "-" + runtime.GOARCH
	wrongChecksum := strings.Repeat("0", sha256.Size*2) + "  " + assetName + "\n"
	var baseURL string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/repos/owner/repository/releases":
			_ = json.NewEncoder(writer).Encode([]map[string]interface{}{{
				"tag_name": "v1.1.0",
				"assets": []map[string]interface{}{
					{"id": 1, "name": assetName, "size": len(asset), "browser_download_url": baseURL + "/asset"},
					{"id": 2, "name": assetName + ".sha256", "size": len(wrongChecksum), "browser_download_url": baseURL + "/checksum"},
				},
			}})
		case "/asset":
			_, _ = writer.Write(asset)
		case "/checksum":
			_, _ = writer.Write([]byte(wrongChecksum))
		}
	}))
	baseURL = server.URL
	defer server.Close()

	updater, err := newVerifiedSelfUpdater(context.Background(), selfUpdateConfig(), trustedClientForTLSServer(server))
	if err != nil {
		t.Fatal(err)
	}
	updater.apiBase = baseURL
	updater.githubToken = ""
	installed := false
	updater.install = func([]byte, string) error {
		installed = true
		return nil
	}
	_, err = updater.UpdateSelf(semver.MustParse("1.0.0"), "owner/repository")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("checksum error = %v", err)
	}
	if installed {
		t.Fatal("checksum mismatch reached installer")
	}
}

func TestSelectLatestUpdateUsesHighestStablePlatformAssetAndRequiresItsChecksum(t *testing.T) {
	validator := &selfupdate.SHA2Validator{}
	assetName := "komari-agent-linux-amd64"
	validAssets := []githubAssetDocument{
		{ID: 1, Name: assetName, Size: 10, BrowserDownloadURL: "https://example.test/asset"},
		{ID: 2, Name: assetName + ".sha256", Size: 80, BrowserDownloadURL: "https://example.test/checksum"},
	}
	releases := []githubReleaseDocument{
		{TagName: "v9.0.0", Draft: true, Assets: validAssets},
		{TagName: "v8.0.0", Prerelease: true, Assets: validAssets},
		{TagName: "v1.0.0", Assets: []githubAssetDocument{{ID: 3, Name: assetName}}},
		{TagName: "v2.0.0", Assets: validAssets},
		{TagName: "v7.0.0", Assets: []githubAssetDocument{{ID: 4, Name: "komari-agent-windows-amd64"}}},
	}
	selected, err := selectLatestUpdate(releases, "owner", "repo", "linux", "amd64", validator)
	if err != nil {
		t.Fatal(err)
	}
	if selected == nil || selected.release.Version.String() != "2.0.0" || selected.validation.ID != 2 {
		t.Fatalf("selected update = %+v", selected)
	}

	releases[3].Assets = releases[3].Assets[:1]
	if _, err := selectLatestUpdate(releases, "owner", "repo", "linux", "amd64", validator); err == nil || !strings.Contains(err.Error(), "missing validation") {
		t.Fatalf("missing checksum error = %v", err)
	}
}

func TestVerifiedUpdaterRejectsUnsafeClientInvalidInputsAndOversizedData(t *testing.T) {
	unsafeTransport := http.DefaultTransport.(*http.Transport).Clone()
	unsafeTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	if _, err := newVerifiedSelfUpdater(context.Background(), selfUpdateConfig(), &http.Client{Transport: unsafeTransport}); err == nil {
		t.Fatal("updater accepted an insecure HTTP client")
	}
	if _, err := newVerifiedSelfUpdater(nil, selfUpdateConfig(), &http.Client{Transport: http.DefaultTransport}); err == nil {
		t.Fatal("updater accepted a nil context")
	}
	if _, _, err := parseRepositorySlug("owner/repo/extra"); err == nil {
		t.Fatal("invalid repository slug accepted")
	}
	if _, err := readBounded(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("oversized response accepted")
	}
	if err := validateDownloadedAsset(&selfupdate.SHA2Validator{}, []byte("asset"), []byte("short")); err == nil {
		t.Fatal("short checksum accepted")
	}
}

func TestVerifiedUpdaterRejectsHTTPSRedirectDowngrade(t *testing.T) {
	plain := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("asset"))
	}))
	defer plain.Close()
	secure := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, plain.URL, http.StatusFound)
	}))
	defer secure.Close()
	client := trustedClientForTLSServer(secure)
	updater, err := newVerifiedSelfUpdater(context.Background(), selfUpdateConfig(), client)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updater.download(secure.URL, 5, 1024); err == nil || !strings.Contains(err.Error(), "non-HTTPS") {
		t.Fatalf("downgrade redirect error = %v", err)
	}
}

func TestVerifiedUpdaterRequestsHonorGlobalContextBudget(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	updater, err := newVerifiedSelfUpdater(ctx, selfUpdateConfig(), trustedClientForTLSServer(server))
	if err != nil {
		t.Fatal(err)
	}
	updater.apiBase = server.URL
	updater.githubToken = ""
	started := time.Now()
	_, err = updater.detectLatest("owner/repository")
	if err == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v, context = %v", err, ctx.Err())
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("update budget took %s", elapsed)
	}
}
