package cmd

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

	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check for necessary dependencies and tools",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("Checking environment...")

		// Check C++ compiler
		checkCompiler()

		// Check flatc
		checkFlatc()
	},
}

func init() {
	rootCmd.AddCommand(doctorCmd)
}

func getFlatcVersion(path string) (string, error) {
	// Verify executable
	if _, err := os.Stat(path); err != nil {
		return "", err
	}

	cmd := exec.Command(path, "--version")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to run flatc --version: %w", err)
	}

	// Output: flatc version 25.9.23
	s := strings.TrimSpace(string(out))
	parts := strings.Split(s, " ")
	if len(parts) < 3 {
		return "", fmt.Errorf("unknown flatc version output: %s", s)
	}
	ver := parts[2]
	if !strings.HasPrefix(ver, "v") {
		ver = "v" + ver
	}
	return ver, nil
}

func checkCompiler() {
	fmt.Print("Checking for C++ compiler... ")

	// Check for cl.exe (MSVC)
	if _, err := exec.LookPath("cl.exe"); err == nil {
		fmt.Println("Found MSVC (cl.exe)")
		return
	}

	// Check for g++ (MinGW/GCC)
	if _, err := exec.LookPath("g++"); err == nil {
		fmt.Println("Found g++")
		return
	}

	if _, err := exec.LookPath("gcc"); err == nil {
		fmt.Println("Found gcc")
		return
	}

	fmt.Println("NOT FOUND")
	fmt.Println("Warning: No C++ compiler found. You will not be able to build the XLL.")
	if runtime.GOOS == "windows" {
		fmt.Println("Tip: Run `winget install -e --id BrechtSanders.WinLibs.POSIX.UCRT` to install MinGW.")
	}
}

func checkFlatc() {
	fmt.Print("Checking for flatc... ")

	path, ver, err := EnsureFlatc()
	if err != nil {
		fmt.Println("NOT FOUND")
		fmt.Printf("Failed to resolve flatc: %v\n", err)
		return
	}
	fmt.Printf("Found %s (%s)\n", ver, path)
}

// EnsureFlatc checks for flatc in PATH or Cache, and downloads it if missing.
// It returns the absolute path to the flatc binary and its version.
func EnsureFlatc() (string, string, error) {
	// 1. Check in PATH
	if path, err := exec.LookPath("flatc"); err == nil {
		ver, err := getFlatcVersion(path)
		if err == nil {
			return path, ver, nil
		}
	}

	// 2. Check in Cache
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", "", fmt.Errorf("error getting cache dir: %w", err)
	}
	binDir := filepath.Join(cacheDir, "xll-gen", "bin")
	exeName := "flatc"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	flatcPath := filepath.Join(binDir, exeName)

	if _, err := os.Stat(flatcPath); err == nil {
		ver, err := getFlatcVersion(flatcPath)
		if err == nil {
			return flatcPath, ver, nil
		}
	}

	// 3. Download (Latest)
	fmt.Println("flatc not found. Attempting to download latest...")
	ver, err := downloadFlatc(binDir)
	if err != nil {
		return "", "", err
	}

	return flatcPath, ver, nil
}

type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func downloadFlatc(destDir string) (string, error) {
	resp, err := http.Get("https://api.github.com/repos/google/flatbuffers/releases/latest")
	if err != nil {
		return "", fmt.Errorf("failed to fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch releases: status %s", resp.Status)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", fmt.Errorf("failed to decode release info: %w", err)
	}

	fmt.Printf("Latest version: %s\n", release.TagName)

	var downloadURL string
	var assetName string

	osName := runtime.GOOS
	arch := runtime.GOARCH

	for _, asset := range release.Assets {
		name := asset.Name
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
			downloadURL = asset.BrowserDownloadURL
			assetName = name
			break
		}
	}

	if downloadURL == "" {
		return "", fmt.Errorf("no suitable binary found for %s/%s", osName, arch)
	}

	fmt.Printf("Downloading %s...\n", assetName)

	// Create temp file for zip
	tmpFile, err := os.CreateTemp("", "flatc-*.zip")
	if err != nil {
		return "", fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	// Download
	dlResp, err := http.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("failed to download asset: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to download asset: status %s", dlResp.Status)
	}

	_, err = io.Copy(tmpFile, dlResp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to save zip: %w", err)
	}

	// Unzip
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create bin dir: %w", err)
	}

	r, err := zip.OpenReader(tmpFile.Name())
	if err != nil {
		return "", fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if f.Name == "flatc" || f.Name == "flatc.exe" {
			rc, err := f.Open()
			if err != nil {
				return "", err
			}

			destPath := filepath.Join(destDir, f.Name)
			outFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
			if err != nil {
				rc.Close()
				return "", err
			}

			_, err = io.Copy(outFile, rc)
			outFile.Close()
			rc.Close()

			if err != nil {
				return "", err
			}
			fmt.Printf("Extracted %s to %s\n", f.Name, destPath)
		}
	}

	return release.TagName, nil
}
