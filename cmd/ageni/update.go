package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

	var downloadURL string
	for _, a := range release.Assets {
		if a.Name == wanted {
			downloadURL = a.BrowserDownloadURL
			break
		}
	}
	if downloadURL == "" {
		return fmt.Errorf("no pre-built binary found for %s/%s (asset: %s)", runtime.GOOS, runtime.GOARCH, wanted)
	}

	fmt.Printf("Downloading %s...\n", wanted)
	newBinary, err := downloadAndExtract(downloadURL, assetName, archiveExt)
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
	req, err := http.NewRequest(http.MethodGet, githubReleasesAPI, nil)
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

// downloadAndExtract fetches the archive and extracts the named binary to a
// temporary file. assetName is the binary filename inside the archive; ext is
// either ".tar.gz" or ".zip".
func downloadAndExtract(url, assetName, ext string) (string, error) {
	resp, err := http.Get(url) //nolint:noctx
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download returned %d", resp.StatusCode)
	}

	switch ext {
	case ".tar.gz":
		return extractTarGz(resp.Body, assetName)
	case ".zip":
		// zip needs a seekable source, so buffer to a temp file first.
		tmp, err := os.CreateTemp("", "ageni-update-archive-*.zip")
		if err != nil {
			return "", err
		}
		defer os.Remove(tmp.Name())
		if _, err := io.Copy(tmp, resp.Body); err != nil {
			tmp.Close()
			return "", err
		}
		tmp.Close()
		return extractZip(tmp.Name(), assetName)
	default:
		return "", fmt.Errorf("unknown archive extension: %s", ext)
	}
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
