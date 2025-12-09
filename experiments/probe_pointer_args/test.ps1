#Requires -Version 5.1
<#
.SYNOPSIS
    Automates testing the ProbeDString XLL in Excel.
.DESCRIPTION
    This script automates the process of testing the ProbeDString.xll.
    1.  Sets up paths and file names.
    2.  Terminates any running Excel processes for a clean start.
    3.  Starts a new Excel instance and makes it visible.
    4.  Loads the XLL add-in.
    5.  Opens the test workbook (test1.xlsx).
    6.  Forces recalculation of the workbook.
    7.  Saves the results to new XLSX and CSV files.
    8.  Closes Excel and releases COM objects.
.NOTES
    Ensure that `test1.xlsx` exists and contains the formulas to be tested.
    The script saves results to 'test1_results.xlsx' and 'test1_results.csv'.
#>

$ErrorActionPreference = 'Stop'

# --- Configuration ---
$scriptPath = $PSScriptRoot
$xllName = "ProbePointerArgs.xll"
$testWorkbookName = "test1.xlsx"

$xllPath = Join-Path -Path $scriptPath -ChildPath "build\$xllName"
$testWorkbookPath = Join-Path -Path $scriptPath -ChildPath $testWorkbookName
$resultXlsxPath = Join-Path -Path $scriptPath -ChildPath "test1_results.xlsx"
$resultCsvPath = Join-Path -Path $scriptPath -ChildPath "test1_results.csv"

# --- Pre-flight Checks ---
if (-not (Test-Path -Path $xllPath)) {
    Write-Error "XLL file not found at: $xllPath. Please build the project first."
    exit 1
}
if (-not (Test-Path -Path $testWorkbookPath)) {
    Write-Error "Test workbook not found at: $testWorkbookPath"
    exit 1
}

# --- Main Execution ---
$excel = $null
try {
    # 1. Ensure Excel is closed for a clean state
    Write-Host "Checking for running Excel instances..."
    Get-Process -Name "EXCEL" -ErrorAction SilentlyContinue | Stop-Process -Force
    Write-Host "All Excel processes terminated."

    # 2. Start Excel and make it visible
    Write-Host "Starting Excel..."
    $excel = New-Object -ComObject "Excel.Application"
    $excel.Visible = $true
    $excel.DisplayAlerts = $false

    # 3. Load the XLL Add-in
    Write-Host "Loading XLL from: $xllPath"
    $addIn = $excel.AddIns.Add($xllPath)
    # Even if already in the collection, we ensure it's installed.
    if (-not $addIn.Installed) {
        $addIn.Installed = $true
    }
    # Allow time for add-in to load
    Start-Sleep -Seconds 2

    # 4. Open the test workbook
    Write-Host "Opening test workbook: $testWorkbookPath"
    $workbook = $excel.Workbooks.Open($testWorkbookPath)

    # 5. Recalculate and wait
    Write-Host "Forcing workbook recalculation..."
    $workbook.ForceFullCalculation()
    # Wait for calculations to finish. For complex sheets, this might need to be longer.
    Start-Sleep -Seconds 3

    # 6. Save results
    Write-Host "Saving results to XLSX: $resultXlsxPath"
    $workbook.SaveAs($resultXlsxPath, 51) # 51 corresponds to xlOpenXMLWorkbook

    Write-Host "Saving results to CSV: $resultCsvPath"
    $workbook.SaveAs($resultCsvPath, 6) # 6 corresponds to xlCSV

    Write-Host "Test completed successfully."

}
catch {
    Write-Host "## Full Exception Details ##"
    Write-Output ($_.Exception | Format-List -Force | Out-String)
    Write-Error "An error occurred during the test automation. See details above. Message: $($_.Exception.Message)"
}
finally {
    # 7. Clean up
    if ($excel -ne $null) {
        Write-Host "Closing Excel..."
        if ($workbook -ne $null) { 
            $workbook.Close($false) # Close workbook without saving changes
        }
        $excel.Quit()
        
        if ($workbook -ne $null) {
            [System.Runtime.InteropServices.Marshal]::ReleaseComObject($workbook) | Out-Null
        }
        if ($excel.Workbooks -ne $null) {
            [System.Runtime.InteropServices.Marshal]::ReleaseComObject($excel.Workbooks) | Out-Null
        }
        [System.Runtime.InteropServices.Marshal]::ReleaseComObject($excel) | Out-Null
        
        # Set variables to null after releasing
        $workbook = $null
        $excel = $null
        
        [System.GC]::Collect()
        [System.GC]::WaitForPendingFinalizers()
    }
}

Write-Host "Script finished."
