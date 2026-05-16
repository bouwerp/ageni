package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const githubReleasesAPI = "https://api.github.com/repos/bouwerp/ageni/releases/latest"

type githubRelease struct {
	TagName string        `json:"tag_name"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func runUpdate(currentVersion string) error {
	fmt.Println("Checking for updates...")

	release, err := fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("failed to check for updates: %w", err)
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	current := strings.TrimPrefix(currentVersion, "v")

	fmt.Printf("Current version : %s\n", currentVersion)
	fmt.Printf("Latest version  : %s\n", release.TagName)

	if current != "dev" && current == latest {
		fmt.Println("Already up to date.")
		return nil
	}

	assetName := platformAssetName()
	archiveExt := ".tar.gz"
	if runtime.GOOS == "windows" {
		assetName += ".exe"
		archiveExt = ".zip"
	}
	wanted := assetName + archiveExt

	var downloadURL, shaURL string
	for _, a := range release.Assets {
		switch a.Name {
		case wanted:
			downloadURL = a.BrowserDownloadURL
		case wanted + ".sha256":
			shaURL = a.BrowserDownloadURL
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no pre-built binary found for %s/%s (asset: %s)", runtime.GOOS, runtime.GOARCH, wanted)
	}

	fmt.Printf("Downloading %s...\n", wanted)
	newBinary, err := downloadAndExtract(downloadURL, shaURL, assetName, archiveExt)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer os.Remove(newBinary)

	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("cannot determine current executable path: %w", err)
	}
	execPath, err = filepath.EvalSymlinks(execPath)
	if err != nil {
		return fmt.Errorf("cannot resolve executable symlink: %w", err)
	}

	if err := replaceExecutable(execPath, newBinary); err != nil {
		return fmt.Errorf("failed to replace binary: %w", err)
	}

	fmt.Printf("Updated to %s successfully.\n", release.TagName)
	return nil
}

func fetchLatestRelease() (*githubRelease, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, githubReleasesAPI, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var release githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return nil, err
	}
	return &release, nil
}

func platformAssetName() string {
	return fmt.Sprintf("ageni-%s-%s", runtime.GOOS, runtime.GOARCH)
}

// downloadAndExtract fetches the archive (verifying its SHA-256 checksum if
// shaURL is non-empty) and extracts the named binary to a temporary file.
// assetName is the binary filename inside the archive; ext is either
// ".tar.gz" or ".zip".
func downloadAndExtract(url, shaURL, assetName, ext string) (string, error) {
	// Buffer the archive to a temp file so we can verify its checksum before
	// trusting it (and so .zip extraction has a seekable source).
	tmp, err := os.CreateTemp("", "ageni-update-archive-*"+ext)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		tmp.Close()
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		tmp.Close()
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	if shaURL != "" {
		if err := verifyChecksum(tmp.Name(), shaURL); err != nil {
			return "", err
		}
		fmt.Println("Checksum OK.")
	}

	switch ext {
	case ".tar.gz":
		f, err := os.Open(tmp.Name())
		if err != nil {
			return "", err
		}
		defer f.Close()
		return extractTarGz(f, assetName)
	case ".zip":
		return extractZip(tmp.Name(), assetName)
	default:
		return "", fmt.Errorf("unknown archive extension: %s", ext)
	}
}

func verifyChecksum(archivePath, shaURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, shaURL, nil)
	if err != nil {
		return fmt.Errorf("fetch checksum: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("fetch checksum: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("checksum fetch returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read checksum: %w", err)
	}
	// Format is "<hex>  <filename>".
	expected := strings.TrimSpace(string(body))
	if i := strings.IndexAny(expected, " \t"); i > 0 {
		expected = expected[:i]
	}

	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	actual := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(expected, actual) {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

func extractTarGz(r io.Reader, binaryName string) (string, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return "", fmt.Errorf("gzip open: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("tar read: %w", err)
		}
		if filepath.Base(hdr.Name) != binaryName {
			continue
		}
		return writeTempBinary(tr)
	}
	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

func extractZip(archivePath, binaryName string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("zip open: %w", err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if filepath.Base(f.Name) != binaryName {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", fmt.Errorf("zip read: %w", err)
		}
		defer rc.Close()
		return writeTempBinary(rc)
	}
	return "", fmt.Errorf("binary %q not found in archive", binaryName)
}

func writeTempBinary(r io.Reader) (string, error) {
	tmp, err := os.CreateTemp("", "ageni-update-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write temp: %w", err)
	}
	tmp.Close()
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// replaceExecutable atomically replaces the running binary. Reads the new
// binary, writes it to a staging file beside the target, then renames.
func replaceExecutable(target, newBinary string) error {
	data, err := os.ReadFile(newBinary)
	if err != nil {
		return fmt.Errorf("read new binary: %w", err)
	}

	dir := filepath.Dir(target)
	staged := filepath.Join(dir, ".ageni-update-staged")
	os.Remove(staged)

	if err := os.WriteFile(staged, data, 0o755); err != nil {
		os.Remove(staged)
		return fmt.Errorf("write staged binary: %w", err)
	}
	if err := os.Chmod(staged, 0o755); err != nil {
		os.Remove(staged)
		return fmt.Errorf("chmod staged binary: %w", err)
	}
	if err := os.Rename(staged, target); err != nil {
		os.Remove(staged)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}
