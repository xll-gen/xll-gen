package cmd

import (
	"os"
	"regexp"
	"testing"
)

func TestDoctorUsesSameFlatbuffersVersion(t *testing.T) {
	// 1. Extract version from generate.go
	genBytes, err := os.ReadFile("generate.go")
	if err != nil {
		t.Fatal(err)
	}
	genContent := string(genBytes)

	// Look for GIT_TAG v...
	re := regexp.MustCompile(`GIT_TAG\s+(v[0-9]+\.[0-9]+\.[0-9]+)`)
	matches := re.FindStringSubmatch(genContent)
	if len(matches) < 2 {
		t.Fatalf("Could not find Flatbuffers GIT_TAG in generate.go")
	}
	expectedVersion := matches[1]
	t.Logf("Found expected Flatbuffers version in generate.go: %s", expectedVersion)

	// 2. Check doctor.go
	docBytes, err := os.ReadFile("doctor.go")
	if err != nil {
		t.Fatal(err)
	}
	docContent := string(docBytes)

	// We expect doctor.go to explicitly mention this version
	if !regexp.MustCompile(regexp.QuoteMeta(expectedVersion)).MatchString(docContent) {
		t.Errorf("doctor.go does not seem to contain the pinned version %s. It might be using 'latest', which causes version mismatch.", expectedVersion)
	}
}
