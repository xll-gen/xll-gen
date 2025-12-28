package flatc

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
	"sync"

	"github.com/xll-gen/xll-gen/internal/ui"
	"github.com/xll-gen/xll-gen/internal/versions"
)

var flatcMu sync.Mutex

// EnsureFlatc checks for the presence of the 'flatc' compiler.
// It searches in the system PATH and the user's cache directory.
// If not found, it attempts to download the correct version from GitHub.
//
// Returns:
//   - string: The absolute path to the flatc executable.
//   - error: An error if flatc cannot be found or downloaded.
func EnsureFlatc() (string, error) {
	flatcMu.Lock()
	defer flatcMu.Unlock()

	// Pin to specific version to match CMake configuration
	flatcVersion := versions.FlatBuffers
	requiredVersion := strings.TrimPrefix(flatcVersion, "v")

	if path, err := exec.LookPath("flatc"); err == nil {
		if ver, err := getFlatcVersion(path); err == nil {
			if ver == requiredVersion {
				return path, nil
			}
			fmt.Printf("Note: System flatc found at %s but is version %s (required %s). Ignoring.\n", path, ver, requiredVersion)
		}
	}

	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("error getting cache dir: %w", err)
	}
	binDir := filepath.Join(cacheDir, "xll-gen", "bin", flatcVersion)
	exeName := "flatc"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	flatcPath := filepath.Join(binDir, exeName)

	if _, err := os.Stat(flatcPath); err == nil {
		return flatcPath, nil
	}

	fmt.Println("flatc not found. Attempting to download...")
	if err := downloadFlatc(binDir, flatcVersion); err != nil {
		return "", err
	}

	return flatcPath, nil
}

// getFlatcVersion extracts the version string from the flatc binary.
//
// Parameters:
//   - path: The path to the flatc executable.
//
// Returns:
//   - string: The version string (e.g., "25.9.23").
//   - error: An error if the version cannot be determined.
func getFlatcVersion(path string) (string, error) {
	cmd := exec.Command(path, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	// Expected: "flatc version 25.9.23"
	parts := strings.Fields(string(out))
	if len(parts) >= 3 && parts[0] == "flatc" && parts[1] == "version" {
		return parts[2], nil
	}
	return "", fmt.Errorf("unknown version format: %q", out)
}

type release struct {
	TagName string  `json:"tag_name"`
	Assets  []asset `json:"assets"`
}

type asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

// downloadFlatc downloads and extracts the flatc binary from GitHub.
//
// Parameters:
//   - destDir: The directory to extract the binary to.
//   - version: The version tag to download (e.g., "v25.9.23").
//
// Returns:
//   - error: An error if download or extraction fails.
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

	s := ui.StartSpinner(fmt.Sprintf("Downloading %s...", assetName))
	defer s.Stop()

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
		return fmt.Errorf("failed to create zip reader: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == "flatc" || f.Name == "flatc.exe" {
			rc, err := f.Open()
			if err != nil {
				return err
			}

			// Extract to a temporary file first to prevent race conditions (ETXTBSY)
			// when multiple processes try to download/execute flatc simultaneously.
			tempFile, err := os.CreateTemp(destDir, f.Name+".*.part")
			if err != nil {
				rc.Close()
				return fmt.Errorf("failed to create temp file: %w", err)
			}
			tempName := tempFile.Name()

			_, err = io.Copy(tempFile, rc)
			tempFile.Close()
			rc.Close()

			if err != nil {
				os.Remove(tempName)
				return fmt.Errorf("failed to write to temp file: %w", err)
			}

			if err := os.Chmod(tempName, f.Mode()); err != nil {
				os.Remove(tempName)
				return fmt.Errorf("failed to chmod temp file: %w", err)
			}

			destPath := filepath.Join(destDir, f.Name)
			if err := os.Rename(tempName, destPath); err != nil {
				// On Windows, Rename fails if destPath exists.
				// On Linux, it replaces atomically.
				// If rename fails, check if the destination exists and works.
				// If it does, we assume another process beat us to it.
				os.Remove(tempName) // Clean up our partial file

				// Check if destination exists
				if _, statErr := os.Stat(destPath); statErr != nil {
					// Real failure
					return fmt.Errorf("failed to rename %s to %s: %w", tempName, destPath, err)
				}
				// If it exists, we accept it.
			}

			s.Stop()
			fmt.Printf("Extracted %s to %s\n", f.Name, destPath)
		}
	}

	return nil
}
