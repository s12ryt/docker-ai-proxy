param(
    [ValidateSet('fmt', 'test', 'vet', 'check', 'all')]
    [string]$Task = 'check'
)

$ErrorActionPreference = 'Stop'

function Resolve-GoExe {
    $cmd = Get-Command go -ErrorAction SilentlyContinue
    if ($cmd) {
        return $cmd.Source
    }

    $portable = Join-Path $env:TEMP 'opencode\go\bin\go.exe'
    if (Test-Path -LiteralPath $portable) {
        return $portable
    }

    throw 'go executable not found. Install Go or place portable Go under %TEMP%\opencode\go\bin\go.exe.'
}

$go = Resolve-GoExe
$gofmt = Join-Path (Split-Path -Parent $go) 'gofmt.exe'
if (-not (Test-Path -LiteralPath $gofmt)) {
    $gofmtCmd = Get-Command gofmt -ErrorAction SilentlyContinue
    if (-not $gofmtCmd) {
        throw 'gofmt executable not found next to go or on PATH.'
    }
    $gofmt = $gofmtCmd.Source
}

$goFiles = @(
    'cmd\ai-hub\main.go',
    'internal\config\config.go',
    'internal\config\config_test.go',
    'internal\providers\keys.go',
    'internal\providers\keys_test.go',
    'internal\protocol\anthropic.go',
    'internal\protocol\anthropic_test.go',
    'internal\protocol\gemini.go',
    'internal\protocol\gemini_test.go',
    'internal\protocol\openai.go',
    'internal\protocol\openai_test.go',
    'internal\protocol\types.go',
    'internal\proxy\proxy.go',
    'internal\proxy\proxy_test.go',
    'internal\proxy\stream.go',
    'internal\proxy\translate.go',
    'internal\proxy\usage.go',
    'internal\server\server.go',
    'internal\server\server_test.go',
    'internal\store\dialect.go',
    'internal\store\dialect_test.go',
    'internal\store\store.go',
    'internal\store\store_test.go'
) | Where-Object { Test-Path -LiteralPath $_ }

function Invoke-Fmt {
    & $gofmt -w @goFiles
}

function Invoke-Test {
    & $go test -count=1 ./...
}

function Invoke-Vet {
    & $go vet ./...
}

switch ($Task) {
    'fmt' { Invoke-Fmt }
    'test' { Invoke-Test }
    'vet' { Invoke-Vet }
    'check' {
        Invoke-Fmt
        Invoke-Test
        Invoke-Vet
    }
    'all' {
        Invoke-Fmt
        Invoke-Test
        Invoke-Vet
    }
}
