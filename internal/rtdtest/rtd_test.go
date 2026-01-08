package rtdtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/xll-gen/sugar"
)

func TestRTDManual(t *testing.T) {
    // This test expects temp_prj to be already built and xll registered (or available)
    // Actually, we should automate the whole thing: init, build, open excel, check value.
    
    projectDir := "temp_prj_test"
    absProjectDir, _ := filepath.Abs(projectDir)
    
    // 1. Clean up and Init
    os.RemoveAll(projectDir)
    
    // Use the compiled xll-gen.exe if available, or go run
    runCmd(t, "go", "run", "../../main.go", "init", projectDir, "-f")
    
    // 2. Modify xll.yaml to add an RTD function if not present
    // (Assuming default init provides a good baseline or we can append)
    
    // 3. Build
    // We need to build the Go server and the C++ XLL
    // Task build is usually available in the generated project
    runCmdIn(t, absProjectDir, "task", "build")
    
    xllPath := filepath.Join(absProjectDir, "build", "temp_prj_test.xll")
    
    // 4. Use Sugar to automate Excel
    excel, err := sugar.NewExcel()
    if err != nil {
        t.Fatalf("Failed to start Excel: %v", err)
    }
    defer excel.Quit()
    
    excel.SetVisible(true)
    
    // Register XLL
    err = excel.RegisterXLL(xllPath)
    if err != nil {
        t.Fatalf("Failed to register XLL: %v", err)
    }
    
    wb, err := excel.NewWorkbook()
    if err != nil {
        t.Fatalf("Failed to create workbook: %v", err)
    }
    
    sheet, _ := wb.ActiveSheet()
    cell, _ := sheet.Cell(1, 1) // A1
    
    // Set RTD formula
    // Assuming the generated RTD function name is based on some convention or we add one
    // Let's assume there is a 'Clock' function or similar in the default template or we add it.
    // For now, let's just try to set a formula and see if it stays "Connecting..."
    
    formula := `=RTD("temp_prj_test.rtd", , "Time")` 
    cell.SetFormula(formula)
    
    fmt.Println("Formula set, waiting for updates...")
    
    // Wait and check
    for i := 0; i < 20; i++ {
        time.Sleep(1 * time.Second)
        val, _ := cell.Value()
        fmt.Printf("Tick %d: Value = %v\n", i, val)
        if val != "Connecting..." && val != nil && val != "" {
            t.Logf("RTD Working! Value: %v", val)
            return
        }
    }
    
    t.Error("RTD Timeout: Value stayed at Connecting...")
}

func runCmd(t *testing.T, name string, args ...string) {
    cmd := exec.Command(name, args...)
    out, err := cmd.CombinedOutput()
    if err != nil {
        t.Fatalf("Command %s failed: %v\nOutput: %s", name, err, string(out))
    }
}

func runCmdIn(t *testing.T, dir string, name string, args ...string) {
    cmd := exec.Command(name, args...)
    cmd.Dir = dir
    out, err := cmd.CombinedOutput()
    if err != nil {
        t.Fatalf("Command %s in %s failed: %v\nOutput: %s", name, dir, err, string(out))
    }
}
