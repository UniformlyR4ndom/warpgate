function push-to-wgclip {
    <#
    .SYNOPSIS
    Pushes text into the warpgate clipboard through the outbox folder watched by an open warpgate tab.
    .DESCRIPTION
    Outbox: $env:WGCLIP_OUTBOX, default ~/wgclip-outbox. Limit: 512 KiB.
    .EXAMPLE
    Get-ChildItem | push-to-wgclip -Title 'dir listing'
    .EXAMPLE
    push-to-wgclip -Path notes.txt
    #>
    [CmdletBinding()]
    param(
        [Parameter(ValueFromPipeline = $true)] $InputObject,
        [string] $Path,
        [string] $Title,
        [switch] $NoWait,
        [int] $TimeoutSec = 30
    )
    end {
        $limit = 512KB
        $tooLarge = "too large (> $($limit / 1KB) KiB); push-to-wgclip is meant for text/small content"
        $outbox = if ($env:WGCLIP_OUTBOX) { $env:WGCLIP_OUTBOX } else { Join-Path $HOME 'wgclip-outbox' }
        if (-not (Test-Path -LiteralPath $outbox -PathType Container)) {
            New-Item -ItemType Directory -Path $outbox -ErrorAction Stop | Out-Null
        }
        $name = 'push-{0}-{1:x8}.txt' -f (Get-Date -Format 'yyyyMMdd-HHmmss-fff'), (Get-Random)
        $file = Join-Path $outbox $name

        if ($Path) {
            $src = Get-Item -LiteralPath $Path -ErrorAction Stop
            if ($src.PSIsContainer) { Write-Error "not a file: $Path"; return }
            if ($src.Length -gt $limit) { Write-Error $tooLarge; return }
            Copy-Item -LiteralPath $src.FullName -Destination $file -ErrorAction Stop
            if (-not $Title) { $Title = $src.Name }
        } else {
            # Stops keeping lines at the limit; chars <= UTF-8 bytes, so this never rejects valid input.
            $n = 0
            $over = $false
            $lines = @($input | Out-String -Stream -Width 4096 | ForEach-Object {
                if (-not $over) {
                    $n += $_.Length + 1
                    if ($n -gt $limit) { $over = $true } else { $_ }
                }
            })
            if ($over) { Write-Error $tooLarge; return }
            $text = ($lines -join "`n").Trim("`r", "`n")
            if ($text.Length -eq 0) { Write-Error 'nothing to push'; return }
            Set-Content -LiteralPath $file -Value $text -Encoding UTF8 -NoNewline -ErrorAction Stop
            if ((Get-Item -LiteralPath $file).Length -gt $limit) {
                Remove-Item -LiteralPath $file
                Write-Error $tooLarge
                return
            }
        }

        $hash = (Get-FileHash -LiteralPath $file -Algorithm SHA256).Hash.ToLower()
        $lines = @("$hash  $name")
        if ($Title) { $lines += $Title -replace '[\x00-\x1f\x7f]', ' ' }
        Set-Content -LiteralPath "$file.sha256" -Value $lines -Encoding UTF8 -ErrorAction Stop

        if ($NoWait) { Write-Host "queued: $name"; return }
        $deadline = (Get-Date).AddSeconds($TimeoutSec)
        while ((Get-Date) -lt $deadline) {
            Start-Sleep -Milliseconds 200
            if (Test-Path -LiteralPath "$file.failed") {
                # the browser may still be checking the renamed file
                Start-Sleep -Seconds 1
                Remove-Item -LiteralPath "$file.failed", $file -ErrorAction SilentlyContinue
                Write-Error 'push failed, see the warpgate tab'
                return
            }
            if (-not (Test-Path -LiteralPath "$file.sha256") -and -not (Test-Path -LiteralPath $file)) {
                Write-Host 'pushed'
                return
            }
        }
        Write-Error "not picked up within $TimeoutSec s: no warpgate tab open, outbox not connected, or auth key not entered? The push stays queued in $outbox."
    }
}
