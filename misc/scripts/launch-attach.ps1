<#
.SYNOPSIS
  Launch an opencode serve + attach pair, killing the server when the
  TUI exits.

.DESCRIPTION
  The default 'opencode serve' is headless and outlives any attached
  TUI. That means after you close the TUI:
    - the server keeps running
    - the a3c-mcp child process the server spawned keeps running
    - the platform broadcast loop keeps injecting messages into a
      session nobody is watching, which the server then feeds to the
      LLM provider — burning tokens with no human in the loop.

  This launcher pairs a fresh 'opencode serve' with the attach you
  actually want. When attach exits (you close the TUI, log out,
  Ctrl-C, etc.) we shoot the server in the head, which collapses the
  whole stack:
      attach -> server -> a3c-mcp -> a3c-mcp poller stops pinging
      the platform; the 7-min heartbeat sweep marks the agent
      offline and releases its tasks/locks/branch.

.PARAMETER Port
  Port for the headless server. Each agent should use a distinct
  port; OpenCode does not multiplex two attaches over the same TUI.

.PARAMETER SessionId
  Optional. Existing session ID to navigate the attach into
  (--session). If omitted, attach picks via its own UI.

.PARAMETER Directory
  Optional working directory for the attach (--dir).

.EXAMPLE
  .\launch-attach.ps1 -Port 4096 -SessionId ses_23c948054ffefWA1nRxgAcbsx7
  Spawns a server on 4096, attaches to that specific session, and
  cleans up the server when the user closes the TUI.

.EXAMPLE
  .\launch-attach.ps1 -Port 4097
  Spawns a server on 4097 and lets the attach pick a session via the
  built-in selector.
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [int]$Port,

    [Parameter()]
    [string]$SessionId,

    [Parameter()]
    [string]$Directory
)

$ErrorActionPreference = 'Stop'

function Stop-ServerSafely {
    param([System.Diagnostics.Process]$Proc)
    if ($null -eq $Proc) { return }
    try {
        if (-not $Proc.HasExited) {
            Write-Host "[launcher] killing server PID $($Proc.Id)"
            Stop-Process -Id $Proc.Id -Force -ErrorAction SilentlyContinue
        }
    } catch {
        Write-Host "[launcher] server kill failed (probably already gone): $($_.Exception.Message)"
    }
}

# 1. Refuse to start if the port is already taken — otherwise the
#    attach below would silently connect to whatever stale server is
#    on that port instead of the one we control.
$existing = netstat -ano | Select-String ":$Port\s.*LISTENING" | Select-Object -First 1
if ($existing) {
    Write-Error "[launcher] port $Port is already in use. Choose another port or stop the existing process. (`netstat -ano | findstr :$Port` to inspect)"
    exit 1
}

# 2. Start the headless server. -PassThru gives us the Process so we
#    can kill it later. -NoNewWindow keeps logs in this terminal so
#    the user can see what is happening.
Write-Host "[launcher] starting opencode serve --port $Port ..."
$serverProc = Start-Process -FilePath 'opencode' `
    -ArgumentList @('serve', '--port', "$Port") `
    -PassThru -NoNewWindow

# Always tear down the server when this script exits, no matter how
# (normal exit, attach crash, Ctrl-C). The trap covers Ctrl-C; the
# finally-equivalent in PowerShell is the script-end cleanup below.
Register-EngineEvent PowerShell.Exiting -Action {
    Stop-ServerSafely -Proc $using:serverProc
} | Out-Null

try {
    # 3. Wait until the server actually answers. Don't block forever —
    #    if the server died on startup, fail fast.
    $deadline = (Get-Date).AddSeconds(15)
    $ready = $false
    while ((Get-Date) -lt $deadline) {
        if ($serverProc.HasExited) {
            Write-Error "[launcher] server exited before becoming ready (code=$($serverProc.ExitCode))"
            exit 2
        }
        $code = curl.exe --max-time 1 -s -o NUL -w '%{http_code}' "http://127.0.0.1:$Port/session" 2>$null
        if ($code -eq '200') {
            $ready = $true
            break
        }
        Start-Sleep -Milliseconds 300
    }
    if (-not $ready) {
        Stop-ServerSafely -Proc $serverProc
        Write-Error "[launcher] server on port $Port did not become ready within 15s"
        exit 3
    }
    Write-Host "[launcher] server ready on http://127.0.0.1:$Port"

    # 4. Build attach args.
    $attachArgs = @('attach', "http://127.0.0.1:$Port")
    if ($SessionId) { $attachArgs += @('-s', $SessionId) }
    if ($Directory) { $attachArgs += @('--dir', $Directory) }

    Write-Host "[launcher] running: opencode $($attachArgs -join ' ')"
    Write-Host "[launcher] (server will be killed automatically when you close the TUI)"
    Write-Host ''

    # 5. Run attach in the foreground. We use Start-Process -Wait so
    #    PowerShell relays its stdio cleanly and we get an exit code.
    $attachProc = Start-Process -FilePath 'opencode' `
        -ArgumentList $attachArgs `
        -NoNewWindow -PassThru -Wait

    Write-Host ''
    Write-Host "[launcher] attach exited with code $($attachProc.ExitCode)"
}
finally {
    # 6. Always reap the server. This runs after attach exits OR after
    #    an exception (e.g. attach crashed mid-startup).
    Stop-ServerSafely -Proc $serverProc
    Write-Host '[launcher] cleanup complete.'
}
