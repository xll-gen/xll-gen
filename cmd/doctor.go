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

	// 1. Check in PATH
	if path, err := exec.LookPath("flatc"); err == nil {
		fmt.Printf("Found in PATH (%s)\n", path)
		return
	}

	// 2. Check in Cache
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		fmt.Printf("Error getting cache dir: %v\n", err)
		return
	}
	binDir := filepath.Join(cacheDir, "xll-gen", "bin")
	exeName := "flatc"
	if runtime.GOOS == "windows" {
		exeName += ".exe"
	}
	flatcPath := filepath.Join(binDir, exeName)

	if _, err := os.Stat(flatcPath); err == nil {
		fmt.Printf("Found in cache (%s)\n", flatcPath)
		return
	}

	fmt.Println("NOT FOUND")
	fmt.Println("Attempting to download flatc...")
	if err := downloadFlatc(binDir); err != nil {
		fmt.Printf("Failed to download flatc: %v\n", err)
	} else {
		fmt.Printf("Successfully downloaded flatc to %s\n", binDir)
	}
}

type Release struct {
	TagName string  `json:"tag_name"`
	Assets  []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func downloadFlatc(destDir string) error {
	resp, err := http.Get("https://api.github.com/repos/google/flatbuffers/releases/latest")
	if err != nil {
		return fmt.Errorf("failed to fetch releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to fetch releases: status %s", resp.Status)
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("failed to decode release info: %w", err)
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
