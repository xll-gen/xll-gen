package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepro_StringOptional_Bug(t *testing.T) {
	// 1. Setup temp dir
	tempDir := t.TempDir()
	origWd, _ := os.Getwd()
	defer os.Chdir(origWd)
	if err := os.Chdir(tempDir); err != nil {
		t.Fatal(err)
	}

	// 2. Init
	projectName := "repro_opt_str"
	if err := runInit(projectName); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	if err := os.Chdir(projectName); err != nil {
		t.Fatalf("Chdir failed: %v", err)
	}

	// 3. Create xll.yaml with string? argument
	xllYaml := `
project:
  name: "repro_opt_str"
  version: "0.1.0"
gen:
  go:
    package: "generated"
functions:
  - name: "TestFunc"
    description: "Test function"
    args:
      - name: "req"
        type: "string"
      - name: "opt"
        type: "string?"
    return: "string"
`
	if err := os.WriteFile("xll.yaml", []byte(xllYaml), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Generate
	if err := runGenerate(); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	// 5. Verify xll_main.cpp content
	content, err := os.ReadFile(filepath.Join("generated", "cpp", "xll_main.cpp"))
	if err != nil {
		t.Fatal(err)
	}
	code := string(content)

	// Check xlfRegister string
	// Expected for string, string? -> "QD%Q" or similar (if return is string=Q)
	// Return string is Q (XLOPER12) or D%?
	// generate.go: lookupXllType: string -> Q.
	// Args: string -> D%, string? -> D% (CURRENT BUG)
	// So we expect "QD%D%$" (Thread safe $)

	// We WANT string? to be Q (or U). Let's say Q.
	// So we WANT "QD%Q$"

	// Find the registration line
	// Expected for string, string? -> "QD%Q$" (Fixed)
    if strings.Contains(code, `TempStr12(L"QD%Q$")`) {
		t.Log("Confirmed: string? is registered as Q (XLOPER12), supporting missing arguments.")
	} else if strings.Contains(code, `TempStr12(L"QD%D%$")`) {
		t.Fatal("Failed: string? is still registered as D% (Counted String). Fix was not applied.")
	} else {
        t.Logf("Could not find exact signature string. Code snippet:\n%s", code)
        t.Errorf("Could not verify fix. Expected signature 'QD%%Q$'.")
    }

    // Check C++ signature
    // void __stdcall TestFunc(const wchar_t* req, LPXLOPER12 opt)
    if strings.Contains(code, "LPXLOPER12 opt") {
         t.Log("Confirmed: string? maps to LPXLOPER12")
    } else {
         t.Error("Expected string? to map to LPXLOPER12 in C++ after fix")
    }
}
