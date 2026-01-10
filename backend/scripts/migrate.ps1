param(
  [string]$DatabaseUrl = $env:DATABASE_URL
)

$ErrorActionPreference = "Stop"

if ([string]::IsNullOrWhiteSpace($DatabaseUrl)) {
  Write-Error "DATABASE_URL is required"
}

$psql = Get-Command psql -ErrorAction SilentlyContinue
if (-not $psql) {
  Write-Error "psql is required on PATH"
}

$migrationsDir = Join-Path $PSScriptRoot "..\\migrations"
$files = Get-ChildItem -Path $migrationsDir -Filter "*.up.sql" | Sort-Object Name
if (-not $files) {
  Write-Error "no migrations found"
}

foreach ($file in $files) {
  Write-Host "Applying $($file.Name)"
  & $psql.Path $DatabaseUrl -v ON_ERROR_STOP=1 -f $file.FullName
}
