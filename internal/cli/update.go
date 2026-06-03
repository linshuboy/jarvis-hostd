package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"agi/runtime/hostd/internal/buildinfo"
	"agi/runtime/hostd/internal/config"
	"agi/runtime/hostd/internal/host"
)

const defaultReleaseManifestURL = "https://github.com/linshuboy/jarvisai-releases/releases/latest/download/release-manifest.json"

type releaseManifest struct {
	Release releaseInfo    `json:"release"`
	Clients releaseClients `json:"clients"`
}

type releaseInfo struct {
	Version          string `json:"version"`
	Channel          string `json:"channel"`
	SourceRepository string `json:"sourceRepository"`
	SourceSHA        string `json:"sourceSha"`
	CreatedAt        string `json:"createdAt"`
}

type releaseClients struct {
	Hostd []releaseAsset `json:"hostd"`
}

type releaseAsset struct {
	Name      string `json:"name"`
	Component string `json:"component"`
	Platform  string `json:"platform"`
	Arch      string `json:"arch"`
	Kind      string `json:"kind"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
}

func updateCommand(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("update subcommand is required")
	}
	switch args[0] {
	case "check":
		return updateCheck(ctx, args[1:], stdout, stderr)
	case "download":
		return updateDownload(ctx, args[1:], stdout, stderr)
	case "apply":
		return updateApply(ctx, args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unsupported update subcommand: %s", args[0])
	}
}

func updateCheck(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("update check", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestURL := flags.String("manifest-url", defaultReleaseManifestURL, "release manifest url")
	proxyURL := flags.String("proxy-url", "", "HTTP(S) proxy url or url template containing {url} for release downloads")
	if err := flags.Parse(args); err != nil {
		return err
	}
	normalizedProxyURL, err := normalizeUpdateProxyURL(*proxyURL)
	if err != nil {
		return err
	}
	normalizedManifestURL, manifest, err := fetchReleaseManifest(ctx, *manifestURL, normalizedProxyURL)
	if err != nil {
		return err
	}
	asset := selectHostdReleaseAsset(manifest)
	return printJSON(stdout, updateCheckPayload(normalizedManifestURL, normalizedProxyURL, manifest, asset))
}

func updateDownload(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("update download", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestURL := flags.String("manifest-url", defaultReleaseManifestURL, "release manifest url")
	proxyURL := flags.String("proxy-url", "", "HTTP(S) proxy url or url template containing {url} for release downloads")
	outputDir := flags.String("output-dir", "", "directory to download the hostd package into")
	if err := flags.Parse(args); err != nil {
		return err
	}
	normalizedProxyURL, err := normalizeUpdateProxyURL(*proxyURL)
	if err != nil {
		return err
	}
	normalizedManifestURL, manifest, err := fetchReleaseManifest(ctx, *manifestURL, normalizedProxyURL)
	if err != nil {
		return err
	}
	asset := selectHostdReleaseAsset(manifest)
	if asset == nil {
		return fmt.Errorf("no hostd release asset matches %s/%s", runtime.GOOS, normalizedArch(runtime.GOARCH))
	}
	targetDir := strings.TrimSpace(*outputDir)
	if targetDir == "" {
		targetDir, err = defaultDownloadDir()
		if err != nil {
			return err
		}
	}
	packagePath, verified, err := downloadAsset(ctx, *asset, targetDir, normalizedProxyURL)
	if err != nil {
		return err
	}
	return printJSON(stdout, map[string]any{
		"manifest_url":      normalizedManifestURL,
		"proxy_url":         emptyStringNil(normalizedProxyURL),
		"release_version":   manifest.Release.Version,
		"asset":             asset,
		"download_path":     packagePath,
		"sha256_verified":   verified,
		"downloaded_at":     time.Now().UTC().Format(time.RFC3339),
		"current_version":   buildinfo.RuntimeVersion(),
		"update_available":  manifest.Release.Version != "" && manifest.Release.Version != buildinfo.RuntimeVersion(),
		"one_click_capable": false,
		"one_click_note":    "download only; use hostd update apply when a local host update command is configured",
	})
}

func updateApply(ctx context.Context, args []string, stdout io.Writer, stderr io.Writer) error {
	flags := flag.NewFlagSet("update apply", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestURL := flags.String("manifest-url", defaultReleaseManifestURL, "release manifest url")
	proxyURL := flags.String("proxy-url", "", "HTTP(S) proxy url or url template containing {url} for release downloads")
	options, err := parseRunOptions(flags, args)
	if err != nil {
		return err
	}
	normalizedProxyURL, err := normalizeUpdateProxyURL(*proxyURL)
	if err != nil {
		return err
	}
	normalizedManifestURL, manifest, err := fetchReleaseManifest(ctx, *manifestURL, normalizedProxyURL)
	if err != nil {
		return err
	}
	asset := selectHostdReleaseAsset(manifest)
	if asset == nil {
		return fmt.Errorf("no hostd release asset matches %s/%s", runtime.GOOS, normalizedArch(runtime.GOARCH))
	}
	loaded, err := config.Load(options)
	if err != nil {
		return err
	}
	updateOptions := hostUpdateOptions(loaded.Config.Components.Host.Update)
	if len(updateOptions.Command) == 0 {
		return fmt.Errorf("host update command is not configured")
	}
	component, err := host.NewComponent(host.Options{
		ComponentID:        host.ComponentID,
		RuntimeVersion:     buildinfo.RuntimeVersion(),
		Methods:            append([]string(nil), loaded.Config.Components.Host.Methods...),
		WorkspaceHints:     hostWorkspaceHints(loaded.Config.Components.Host.WorkspaceHints),
		MaxReadBytes:       host.DefaultMaxReadBytes,
		MaxOutputBytes:     host.DefaultMaxOutputBytes,
		DefaultExecTimeout: host.DefaultExecTimeout,
		Update:             updateOptions,
	})
	if err != nil {
		return err
	}
	restoreProxyEnv := applyUpdateProxyEnv(normalizedProxyURL)
	defer restoreProxyEnv()
	result, updateErr := component.Dispatch(host.HostUpdateMethod, map[string]any{
		"componentId":        host.ComponentID,
		"runtime_id":         "",
		"release_tag":        manifest.Release.Version,
		"source_repository":  manifest.Release.SourceRepository,
		"source_sha":         manifest.Release.SourceSHA,
		"package_url":        updateFetchURL(asset.URL, normalizedProxyURL),
		"package_sha256":     asset.SHA256,
		"artifact_base_url":  artifactBaseURL(updateFetchURL(asset.URL, normalizedProxyURL)),
		"release_manifest":   normalizedManifestURL,
		"release_proxy_url":  normalizedProxyURL,
		"release_created_at": manifest.Release.CreatedAt,
		"asset":              asset,
	})
	if updateErr != nil {
		return fmt.Errorf("%s: %s", updateErr.Code, updateErr.Message)
	}
	return printJSON(stdout, result)
}

func updateCheckPayload(manifestURL string, proxyURL string, manifest releaseManifest, asset *releaseAsset) map[string]any {
	return map[string]any{
		"manifest_url":      manifestURL,
		"proxy_url":         emptyStringNil(proxyURL),
		"current_version":   buildinfo.RuntimeVersion(),
		"latest_version":    manifest.Release.Version,
		"update_available":  manifest.Release.Version != "" && manifest.Release.Version != buildinfo.RuntimeVersion(),
		"checked_at":        time.Now().UTC().Format(time.RFC3339),
		"asset":             asset,
		"all_assets":        manifest.Clients.Hostd,
		"one_click_capable": false,
		"one_click_note":    "requires configured host update command; run hostd update apply to execute it",
	}
}

func normalizeManifestURL(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		trimmed = defaultReleaseManifestURL
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid release manifest url %q: %w", trimmed, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("release manifest url scheme must be http or https, got %s", parsed.Scheme)
	}
	return parsed.String(), nil
}

func normalizeUpdateProxyURL(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", nil
	}
	if strings.Contains(trimmed, "{url}") {
		sample := strings.ReplaceAll(trimmed, "{url}", "https%3A%2F%2Fexample.com%2Ffile")
		parsed, err := url.Parse(sample)
		if err != nil {
			return "", fmt.Errorf("invalid update proxy url template %q: %w", trimmed, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return "", fmt.Errorf("update proxy url template scheme must be http or https, got %s", parsed.Scheme)
		}
		return trimmed, nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("invalid update proxy url %q: %w", trimmed, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("update proxy url scheme must be http or https, got %s", parsed.Scheme)
	}
	return parsed.String(), nil
}

func updateFetchURL(targetURL string, proxyURL string) string {
	trimmedProxy := strings.TrimSpace(proxyURL)
	if trimmedProxy == "" {
		return strings.TrimSpace(targetURL)
	}
	if strings.Contains(trimmedProxy, "{url}") {
		return strings.ReplaceAll(trimmedProxy, "{url}", url.QueryEscape(strings.TrimSpace(targetURL)))
	}
	return strings.TrimSpace(targetURL)
}

func updateHTTPClient(proxyURL string) (*http.Client, error) {
	trimmedProxy := strings.TrimSpace(proxyURL)
	if trimmedProxy == "" || strings.Contains(trimmedProxy, "{url}") {
		return http.DefaultClient, nil
	}
	parsed, err := url.Parse(trimmedProxy)
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(parsed),
		},
	}, nil
}

func applyUpdateProxyEnv(proxyURL string) func() {
	trimmedProxy := strings.TrimSpace(proxyURL)
	if trimmedProxy == "" || strings.Contains(trimmedProxy, "{url}") {
		return func() {}
	}
	previousHTTP, hadHTTP := os.LookupEnv("HTTP_PROXY")
	previousHTTPS, hadHTTPS := os.LookupEnv("HTTPS_PROXY")
	_ = os.Setenv("HTTP_PROXY", trimmedProxy)
	_ = os.Setenv("HTTPS_PROXY", trimmedProxy)
	return func() {
		if hadHTTP {
			_ = os.Setenv("HTTP_PROXY", previousHTTP)
		} else {
			_ = os.Unsetenv("HTTP_PROXY")
		}
		if hadHTTPS {
			_ = os.Setenv("HTTPS_PROXY", previousHTTPS)
		} else {
			_ = os.Unsetenv("HTTPS_PROXY")
		}
	}
}

func emptyStringNil(value string) any {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func fetchReleaseManifest(ctx context.Context, manifestURL string, proxyURL string) (string, releaseManifest, error) {
	normalized, err := normalizeManifestURL(manifestURL)
	if err != nil {
		return "", releaseManifest{}, err
	}
	requestURL := updateFetchURL(normalized, proxyURL)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return "", releaseManifest{}, err
	}
	request.Header.Set("Accept", "application/json")
	client, err := updateHTTPClient(proxyURL)
	if err != nil {
		return "", releaseManifest{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", releaseManifest{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", releaseManifest{}, fmt.Errorf("release manifest request failed with status %s", response.Status)
	}
	var manifest releaseManifest
	decoder := json.NewDecoder(io.LimitReader(response.Body, 20*1024*1024))
	if err := decoder.Decode(&manifest); err != nil {
		return "", releaseManifest{}, fmt.Errorf("failed to decode release manifest: %w", err)
	}
	return normalized, manifest, nil
}

func selectHostdReleaseAsset(manifest releaseManifest) *releaseAsset {
	platform := runtime.GOOS
	arch := normalizedArch(runtime.GOARCH)
	for _, asset := range manifest.Clients.Hostd {
		if asset.Platform == platform && normalizedArch(asset.Arch) == arch {
			copy := asset
			return &copy
		}
	}
	for _, asset := range manifest.Clients.Hostd {
		if asset.Platform == platform {
			copy := asset
			return &copy
		}
	}
	return nil
}

func normalizedArch(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "x64", "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func defaultDownloadDir() (string, error) {
	if runtime.GOOS == "windows" {
		if value := strings.TrimSpace(os.Getenv("USERPROFILE")); value != "" {
			return filepath.Join(value, "Downloads"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Downloads"), nil
}

func safeDownloadName(name string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	value := strings.Trim(replacer.Replace(strings.TrimSpace(name)), ". ")
	if value == "" {
		return "hostd-update"
	}
	return value
}

func uniqueDownloadPath(directory string, name string) string {
	safeName := safeDownloadName(name)
	candidate := filepath.Join(directory, safeName)
	if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
		return candidate
	}
	extension := filepath.Ext(safeName)
	stem := strings.TrimSuffix(safeName, extension)
	for index := 1; index < 1000; index++ {
		nextName := fmt.Sprintf("%s-%d%s", stem, index, extension)
		candidate = filepath.Join(directory, nextName)
		if _, err := os.Stat(candidate); errors.Is(err, os.ErrNotExist) {
			return candidate
		}
	}
	return filepath.Join(directory, fmt.Sprintf("%s-%d", safeName, os.Getpid()))
}

func downloadAsset(ctx context.Context, asset releaseAsset, directory string, proxyURL string) (string, bool, error) {
	requestURL := updateFetchURL(asset.URL, proxyURL)
	parsed, err := url.Parse(strings.TrimSpace(requestURL))
	if err != nil {
		return "", false, fmt.Errorf("invalid release asset url %q: %w", asset.URL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", false, fmt.Errorf("release asset url scheme must be http or https, got %s", parsed.Scheme)
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return "", false, err
	}
	target := uniqueDownloadPath(directory, asset.Name)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return "", false, err
	}
	client, err := updateHTTPClient(proxyURL)
	if err != nil {
		return "", false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", false, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", false, fmt.Errorf("client package download failed with status %s", response.Status)
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", false, err
	}
	hasher := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(file, hasher), response.Body)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil {
		_ = os.Remove(target)
		return "", false, copyErr
	}
	if syncErr != nil {
		_ = os.Remove(target)
		return "", false, syncErr
	}
	if closeErr != nil {
		_ = os.Remove(target)
		return "", false, closeErr
	}
	expected := strings.ToLower(strings.TrimSpace(asset.SHA256))
	if expected == "" {
		return target, false, nil
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expected {
		_ = os.Remove(target)
		return "", false, fmt.Errorf("downloaded client package sha256 mismatch: expected %s, got %s", expected, actual)
	}
	return target, true, nil
}

func artifactBaseURL(assetURL string) string {
	parsed, err := url.Parse(assetURL)
	if err != nil {
		return ""
	}
	parsed.RawQuery = ""
	parsed.Fragment = ""
	path := parsed.Path
	if index := strings.LastIndex(path, "/"); index >= 0 {
		parsed.Path = path[:index+1]
	}
	return parsed.String()
}
