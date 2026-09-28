# Windows clean-environment smoke test for the production installer.
#   1. silent install of the single self-contained installer (no network needed)
#   2. verify installed files and the Add/Remove Programs entry
#   3. start the installed application core headless, check its API,
#      boot Android (TCG fallback on runners without nested virtualization)
#   4. silent uninstall and verify it is clean
param(
    [Parameter(Mandatory = $true)][string]$Installer,
    [int]$BootTimeoutMinutes = 40
)
$ErrorActionPreference = "Stop"
$installDir = Join-Path $env:ProgramFiles "droidpector"
$work = Join-Path $env:RUNNER_TEMP "apkinspector-smoke"
New-Item -ItemType Directory -Force -Path $work | Out-Null
Copy-Item $Installer (Join-Path $work "setup.exe")

Write-Host "== silent install"
$p = Start-Process -FilePath (Join-Path $work "setup.exe") -ArgumentList "/S" -Wait -PassThru
if ($p.ExitCode -ne 0) { throw "installer exit code $($p.ExitCode)" }
foreach ($f in @("droidpector.exe", "Uninstall.exe", "runtime\qemu\qemu-system-x86_64.exe", "runtime\qemu\qemu-img.exe",
                 "runtime\android\x86_64\system.iso", "runtime\android\x86_64\kernel", "runtime\android\x86_64\runtime.json",
                 "runtime\licenses\NOTICE.txt")) {
    if (-not (Test-Path (Join-Path $installDir $f))) { throw "missing installed file: $f" }
}
$key = "HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\droidpector"
if (-not (Test-Path $key)) { throw "uninstall registry entry missing" }

Write-Host "== QEMU runs from the install folder (no system dependencies)"
$v = & (Join-Path $installDir "runtime\qemu\qemu-system-x86_64.exe") --version
if ($LASTEXITCODE -ne 0) { throw "bundled QEMU does not start" }
Write-Host $v[0]

Write-Host "== start the application core"
$env:DROIDPECTOR_HOME = Join-Path $work "data"
$psi = New-Object System.Diagnostics.ProcessStartInfo
$psi.FileName = Join-Path $installDir "droidpector.exe"
$psi.Arguments = "--headless"
$psi.RedirectStandardOutput = $true
$psi.RedirectStandardError = $true
$psi.UseShellExecute = $false
$proc = [System.Diagnostics.Process]::Start($psi)
$line = $proc.StandardOutput.ReadLine()
$ready = $line | ConvertFrom-Json
$h = @{ Authorization = "Bearer $($ready.token)" }
$info = Invoke-RestMethod -Uri "$($ready.base)/api/info" -Headers $h
if (-not ($info.runtimes | Where-Object { $_.name -eq "x86_64" })) { throw "x86_64 runtime not detected" }
$st = Invoke-RestMethod -Uri "$($ready.base)/api/status" -Headers $h
if ($st.state -ne "stopped") { throw "unexpected initial state $($st.state)" }
# Unauthenticated access must be refused.
try { Invoke-RestMethod -Uri "$($ready.base)/api/status"; throw "API accepted a request without token" } catch { if ($_.Exception.Response.StatusCode.value__ -ne 401) { throw } }

Write-Host "== boot Android"
Invoke-RestMethod -Method Post -Uri "$($ready.base)/api/sandbox/start" -Headers $h -Body "{}" -ContentType "application/json" | Out-Null
$deadline = (Get-Date).AddMinutes($BootTimeoutMinutes)
do {
    Start-Sleep -Seconds 10
    $st = Invoke-RestMethod -Uri "$($ready.base)/api/status" -Headers $h
    Write-Host "  $($st.state): $($st.message) [accel=$($st.accelerator)]"
    if ($st.state -eq "error") { throw "sandbox error: $($st.error.title) $($st.error.details)" }
} while ($st.state -ne "ready" -and (Get-Date) -lt $deadline)
if ($st.state -ne "ready") { throw "Android did not become ready within $BootTimeoutMinutes minutes" }
if (-not $st.captureActive) { throw "capture not active" }

Invoke-RestMethod -Method Post -Uri "$($ready.base)/api/sandbox/stop" -Headers $h | Out-Null
Start-Sleep -Seconds 15
$proc.Kill()
$proc.WaitForExit()
if (Get-Process qemu-system-x86_64 -ErrorAction SilentlyContinue) { throw "QEMU survived the application (job object not effective)" }

Write-Host "== silent uninstall"
$p = Start-Process -FilePath (Join-Path $installDir "Uninstall.exe") -ArgumentList "/S _?=$installDir" -Wait -PassThru
if ($p.ExitCode -ne 0) { throw "uninstaller exit code $($p.ExitCode)" }
Remove-Item (Join-Path $installDir "Uninstall.exe") -ErrorAction SilentlyContinue
Remove-Item $installDir -ErrorAction SilentlyContinue
if (Test-Path (Join-Path $installDir "runtime")) { throw "runtime folder left behind" }
if (Test-Path $key) { throw "uninstall registry entry left behind" }
Write-Host "Windows smoke test passed."
