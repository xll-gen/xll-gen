package rtdtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xll-gen/sugar"
	"github.com/xll-gen/sugar/excel"
)

func TestRTDStockQuote(t *testing.T) {
	// 1. Setup Environment
	// Cleanup existing processes to avoid file lock
	exec.Command("taskkill", "/F", "/IM", "excel.exe", "/T").Run()
	// Kill any process that looks like our test server
	exec.Command("powershell", "-Command", "Get-Process temp_prj_rtd* -ErrorAction SilentlyContinue | Stop-Process -Force").Run()
	time.Sleep(1 * time.Second)

	prjName := "temp_prj_rtd"
	baseDir, _ := os.Getwd()
	if filepath.Base(baseDir) == "rtdtest" {
		baseDir = filepath.Dir(filepath.Dir(baseDir))
	}

	xllGenBin := filepath.Join(baseDir, "xll-gen.exe")
	// Build the tool
	buildToolCmd := exec.Command("go", "build", "-o", xllGenBin, ".")
	buildToolCmd.Dir = baseDir
	if out, err := buildToolCmd.CombinedOutput(); err != nil {
		t.Fatalf("Failed to build xll-gen tool: %v\n%s", err, string(out))
	}
	// defer os.Remove(xllGenBin) // Optional cleanup

	prjDir := filepath.Join(baseDir, prjName)

	// Clean up previous run
	os.RemoveAll(prjDir)

	// 2. Initialize Project
	// Default init already includes StockQuote (rtd) and Add (sync) in xll.yaml
	t.Logf("Initializing project in %s...", prjDir)
	initCmd := exec.Command(xllGenBin, "init", prjName, "--dev", "-f")
	initCmd.Dir = baseDir
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("Init failed: %v\n%s", err, string(out))
	}

	// 3. Inject Server Logic (server.go)
	// Implementation using the individual StockQuote_RTD handler.
	t.Log("Injecting server.go with individual RTD handler...")
	serverGoPath := filepath.Join(prjDir, "server.go")
	serverCode := `package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"time"
	"temp_prj_rtd/generated"

	"github.com/xll-gen/xll-gen/pkg/rtd"
)

type Handler struct{}

func (h *Handler) StockQuote_RTD(ctx context.Context, topicID int32, symbol string) error {
	log.Printf("RTD Connect: StockQuote for %s (Topic %d)", symbol, topicID)
	
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()
		
		price := 100.0
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				price += (rand.Float64() - 0.5) * 2
				val := fmt.Sprintf("%s: %.2f", symbol, price)
				if err := rtd.GlobalRtd.SendUpdate(topicID, val); err != nil {
					log.Printf("RTD update failed for topic %d: %v", topicID, err)
					return
				}
			}
		}
	}()
	return nil
}

func (h *Handler) OnRtdConnect(ctx context.Context, topicID int32, args []string, newValues bool) error { return nil }
func (h *Handler) OnRtdDisconnect(ctx context.Context, topicID int32) error { return nil }
func (h *Handler) OnCalculationEnded(ctx context.Context) error { return nil }
func (h *Handler) OnCalculationCanceled(ctx context.Context) error { return nil }

// Sync functions required by interface
func (h *Handler) Add(ctx context.Context, a int32, b int32) (int32, error) { return a + b, nil }
func (h *Handler) Greet(ctx context.Context, name string) (string, error) { return "Hello " + name, nil }
func (h *Handler) IsEven(ctx context.Context, val int32) (bool, error) { return val%2 == 0, nil }
func (h *Handler) GetPrice(ctx context.Context, ticker string) (float64, error) { return 123.45, nil }

func main() {
	generated.Serve(&Handler{})
}
`
	if err := os.WriteFile(serverGoPath, []byte(serverCode), 0644); err != nil {
		t.Fatalf("Failed to write server.go: %v", err)
	}

	// 4. Build Project
	t.Log("Building project...")
	buildCmd := exec.Command(xllGenBin, "build", "--debug")
	buildCmd.Dir = prjDir

	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("Build failed: %v\n%s", err, string(out))
	}

	// 5. Verify with Excel
	xllPath := filepath.Join(prjDir, "build", "temp_prj_rtd.xll")
	if _, err := os.Stat(xllPath); os.IsNotExist(err) {
		t.Fatalf("XLL not found at %s", xllPath)
	}

	t.Log("Starting Excel...")
	var xlApp excel.Application
	err := sugar.Do(func(ctx *sugar.Context) error {
		xlApp = excel.NewApplication(ctx)
		xlApp.Put("Visible", true)
		
		// Register XLL
		res := xlApp.Call("RegisterXLL", xllPath)
		if res.Err() != nil {
			return res.Err()
		}

		wb := xlApp.Workbooks().Add()
		sheet := wb.ActiveSheet()
		cell := sheet.Cells(1, 1)

		// Ensure Excel is closed after test
		defer func() {
			if r := recover(); r != nil {
				t.Logf("Recovered from panic in Excel automation: %v", r)
			}
			// We can't easily quit here if we use sugar.Do at the top level, 
			// but we can try to call Quit.
			sugar.Do(func(innerCtx *sugar.Context) error {
				if xlApp != nil {
					xlApp.Call("Quit")
				}
				return nil
			})
		}()

		formula := `=StockQuote("AAPL")`
		t.Logf("Setting formula: %s", formula)
		cell.Put("Formula", formula)

		// 6. Watch for updates
		t.Log("Waiting for RTD updates...")
		initialVal := ""
		updated := false

		// Poll for 60 seconds (RTD can sometimes be slow to start in automated environment)
		for i := 0; i < 120; i++ {
			time.Sleep(500 * time.Millisecond)
			
			// Use generic Chain API to get the value to avoid IDispatch conversion error
			var val interface{}
			valRes := cell.Get("Value")
			if valRes.Err() != nil {
				// If it's an IDispatch error, it might be an Error object from Excel
				// We'll skip and retry
				continue
			}
			
			var err error
			val, err = valRes.Value()
			if err != nil {
				// log error but continue
				continue
			}

			strVal := fmt.Sprintf("%v", val)
			// RTD values in Excel are sometimes returned as specialized types or prefixed.
			// Log every unique value change
			if strVal != initialVal {
				t.Logf("Tick %d: Value changed from '%s' to '%s'", i, initialVal, strVal)
				
				// "0" or "#N/A" (empty) are often intermediate states
				if strVal != "Connecting..." && strVal != "<nil>" && strVal != "" && strVal != "0" && !strings.Contains(strVal, "N/A") {
					if initialVal == "" || initialVal == "Connecting..." {
						initialVal = strVal
						continue
					}
					
					if strings.Contains(strVal, "AAPL") {
						t.Logf("SUCCESS: Value updated and contains AAPL: '%s'", strVal)
						updated = true
						break
					}
				}
				initialVal = strVal
			}
		}

		if !updated {
			return fmt.Errorf("RTD value did not update in time. Last value: '%s'", initialVal)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("Excel automation failed: %v", err)
	}
}

