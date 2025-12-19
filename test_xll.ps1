#Requires -Version 5.1
param(
    [string]$ProjectPath = "demo_project"
)

$scriptPath = $PSScriptRoot
$fullProjectPath = Join-Path -Path $scriptPath -ChildPath $ProjectPath
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

    Write-Host "Starting Excel..."
    $excel = New-Object -ComObject "Excel.Application"
    $excel.Visible = $true
    $excel.DisplayAlerts = $false

    $workbook = $excel.Workbooks.Add()
    $sheet = $workbook.ActiveSheet

    Write-Host "Loading XLL Add-in..."
    $addIn = $excel.AddIns.Add($xllPath)
    
    if ($addIn -eq $null) {
        Write-Error "Failed to add Add-in: object is null"
    }
    Write-Host "Add-in object obtained. Name: $($addIn.Name)"

    $addIn.Installed = $true
    Write-Host "Add-in Installed set to true."

    # Give server time to initialize
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

    Write-Host "Waiting for calculation (5s for async)..."
    Start-Sleep -Seconds 5

    $resAdd = $sheet.Cells.Item(2, 3).Value
    $resPrice = $sheet.Cells.Item(3, 3).Value
    $resGreet = $sheet.Cells.Item(4, 3).Value

    Write-Host "`n--- Execution Summary ---"
    Write-Host "Add:   $resAdd"
    Write-Host "Price: $resPrice"
    Write-Host "Greet: $resGreet"

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
