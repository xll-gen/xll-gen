#Requires -Version 5.1
param(
    [string]$ProjectPath = "demo_project"
)

$scriptPath = $PSScriptRoot
# Script is in scripts/ directory, so repo root is one level up
$repoRoot = Split-Path -Parent $scriptPath
$fullProjectPath = Join-Path -Path $repoRoot -ChildPath $ProjectPath
$projectName = Split-Path $fullProjectPath -Leaf
$xllName = "$projectName.xll"

# Debug 빌드 결과물을 우선 확인
$xllPath = Join-Path -Path $fullProjectPath -ChildPath "build\$xllName"
if (-not (Test-Path $xllPath)) {
    # CMake/Ninja 등의 기본 경로 확인
    $xllPath = Join-Path -Path $fullProjectPath -ChildPath "build\cpp\Debug\$xllName"
}
if (-not (Test-Path $xllPath)) {
    # 일반 build 폴더 확인
    $xllPath = Join-Path -Path $fullProjectPath -ChildPath "build\$xllName"
}

if (-not (Test-Path $xllPath)) {
    Write-Error "XLL not found at: $xllPath. Please run 'task build-debug' inside the project directory first."
    exit 1
}

Write-Host "--- xll-gen Debug Test Runner ---"
Write-Host "Project: $projectName"
Write-Host "XLL Path: $xllPath (Debug)"

$excel = $null
try {
    Write-Host "Cleaning up Excel..."
    Get-Process -Name "EXCEL" -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Seconds 1

    Write-Host "Starting Excel with XLL..."
    # Launch Excel directly with the XLL path as an argument
    Start-Process "excel.exe" -ArgumentList "`"$xllPath`""
    
    # Wait for Excel to register itself in the ROT (Running Object Table)
    Write-Host "Waiting for Excel to initialize..."
    $excel = $null
    $retryCount = 0
    $maxRetries = 15
    while ($excel -eq $null -and $retryCount -lt $maxRetries) {
        try {
            $excel = [Runtime.InteropServices.Marshal]::GetActiveObject("Excel.Application")
        }
        catch {
            Start-Sleep -Seconds 1
            $retryCount++
        }
    }

    if ($excel -eq $null) {
        throw "Failed to connect to running Excel instance after $maxRetries seconds."
    }

    $excel.Visible = $true
    $excel.DisplayAlerts = $false

    # Ensure we have a workbook to test with
    if ($excel.Workbooks.Count -eq 0) {
        $workbook = $excel.Workbooks.Add()
    } else {
        $workbook = $excel.Workbooks.Item(1)
    }
    
    $sheet = $workbook.ActiveSheet

    # Give XLL/Server time to initialize
    Write-Host "Excel connected. Waiting for XLL server initialization..."
    Start-Sleep -Seconds 3

    Write-Host "Injecting test formulas..."
    $sheet.Cells.Item(1, 1).Value = "Function"
    $sheet.Cells.Item(1, 2).Value = "Formula"
    $sheet.Cells.Item(1, 3).Value = "Result"

    # Common functions for default template
    $sheet.Cells.Item(2, 1).Value = "Add(10, 20)"
    $sheet.Cells.Item(2, 2).Formula = "=Add(10, 20)"

    $sheet.Cells.Item(3, 1).Value = "GetPrice('AAPL')"
    $sheet.Cells.Item(3, 2).Formula = "=GetPrice(`"AAPL`")"

    $sheet.Cells.Item(4, 1).Value = "Greet('Gemini')"
    $sheet.Cells.Item(4, 2).Formula = "=Greet(`"Gemini`")"

    $sheet.Cells.Item(5, 1).Value = "StockQuote('MSFT')"
    $sheet.Cells.Item(5, 2).Formula = "=StockQuote(`"MSFT`")"

    Write-Host "Waiting for calculation (10s for async and RTD)..."
    Start-Sleep -Seconds 10

    $resAdd = $sheet.Cells.Item(2, 3).Text
    $resPrice = $sheet.Cells.Item(3, 3).Text
    $resGreet = $sheet.Cells.Item(4, 3).Text
    $resRTD = $sheet.Cells.Item(5, 3).Text

    Write-Host "`n--- Execution Summary ---"
    Write-Host "Add:   $resAdd"
    Write-Host "Price: $resPrice"
    Write-Host "Greet: $resGreet"
    Write-Host "RTD:   $resRTD"

    if ($resPrice -eq 150) {
        Write-Host "`nSUCCESS: Async function returned correctly." -ForegroundColor Green
    } else {
        Write-Host "`nCHECK: Async function might still be calculating or failed." -ForegroundColor Yellow
    }

    Write-Host "`nTest complete. Excel is active."

}
catch {
    Write-Error "Error: $($_.Exception.Message)"
}
finally {
    if ($excel -ne $null) {
        [System.Runtime.InteropServices.Marshal]::ReleaseComObject($excel) | Out-Null
    }
}
