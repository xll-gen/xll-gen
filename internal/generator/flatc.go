package generator

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// EnsureFlatc checks for flatc in PATH or Cache, and downloads it if missing.
// It returns the absolute path to the flatc binary.
func EnsureFlatc() (string, error) {
	// 1. Check in PATH
	if path, err := exec.LookPath("flatc"); err == nil {
		return path, nil
	}

	// 2. Check in Cache
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("error getting cache dir: %w", err)
	}
	// Pin to specific version to match CMake configuration
	const flatcVersion = "v25.9.23"
	binDir := filepath.Join(cacheDir, "xll-gen", "bin", flatcVersion)
	exeName := "flatc"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	flatcPath := filepath.Join(binDir, exeName)

	if _, err := os.Stat(flatcPath); err == nil {
		return flatcPath, nil
	}

	// 3. Download
	fmt.Println("flatc not found. Attempting to download...")
	if err := downloadFlatc(binDir, flatcVersion); err != nil {
		return "", err
	}

	return flatcPath, nil
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func downloadFlatc(destDir string, version string) error {
	url := "https://api.github.com/repos/google/flatbuffers/releases/tags/" + version
	resp, err := http.Get(url)
	if err != nil {
		return fmt.Errorf("failed to fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch releases: status %s", resp.Status)
	}

	var rel release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return fmt.Errorf("failed to decode release info: %w", err)
	}

	fmt.Printf("Version: %s\n", rel.TagName)

	var downloadURL string
	var assetName string

	osName := runtime.GOOS
	arch := runtime.GOARCH

	for _, a := range rel.Assets {
		name := a.Name
		matched := false

		switch osName {
		case "windows":
			if strings.Contains(name, "Windows.flatc.binary.zip") {
				matched = true
			}
		case "linux":
			if strings.Contains(name, "Linux.flatc.binary.g++") {
				matched = true
			}
		case "darwin":
			if arch == "amd64" {
				if strings.Contains(name, "MacIntel.flatc.binary.zip") {
					matched = true
				}
			} else {
				if strings.Contains(name, "Mac.flatc.binary.zip") {
					matched = true
				}
			}
		}

		if matched {
			downloadURL = a.BrowserDownloadURL
			assetName = name
			break
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("no suitable binary found for %s/%s", osName, arch)
	}

	fmt.Printf("Downloading %s...\n", assetName)

	// Create temp file for zip
	tmpFile, err := os.CreateTemp("", "flatc-*.zip")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	// Download
	dlResp, err := http.Get(downloadURL)
	if err != nil {
		return fmt.Errorf("failed to download asset: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download asset: status %s", dlResp.Status)
	}

	_, err = io.Copy(tmpFile, dlResp.Body)
	if err != nil {
		return fmt.Errorf("failed to save zip: %w", err)
	}

	// Unzip
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return fmt.Errorf("failed to create bin dir: %w", err)
	}

	r, err := zip.OpenReader(tmpFile.Name())
	if err != nil {
		return fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == "flatc" || f.Name == "flatc.exe" {
			rc, err := f.Open()
			if err != nil {
				return err
			}

			destPath := filepath.Join(destDir, f.Name)
			outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				rc.Close()
				return err
			}

			_, err = io.Copy(outFile, rc)
			outFile.Close()
			rc.Close()

			if err != nil {
				return err
			}
			fmt.Printf("Extracted %s to %s\n", f.Name, destPath)
		}
	}

	return nil
}
