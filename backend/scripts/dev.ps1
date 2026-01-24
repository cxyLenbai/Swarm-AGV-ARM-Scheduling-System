param(
  [Parameter(Mandatory = $true)]
  [ValidateSet("api", "core", "py", "consumer", "worker", "congestion")]
  [string]$Target,

  [string]$Env = "dev",
  [string]$Version = "dev",

  [int]$ApiPort = 8080,
  [int]$CorePort = 8081,
  [int]$PyPort = 8000,
  [string]$PyService = "app"
)

$ErrorActionPreference = "Stop"

function Invoke-DevApi {
  $env:SERVICE_NAME = "api"
  $env:ENV = $Env
  $env:PORT = "$ApiPort"
  $env:VERSION = $Version
  Push-Location (Join-Path $PSScriptRoot "..\\go")
  try {
    go run .\\api\\cmd\\api
  } finally {
    Pop-Location
  }
}

function Invoke-DevCore {
  $env:SERVICE_NAME = "core"
  $env:ENV = $Env
  $env:PORT = "$CorePort"
  $env:VERSION = $Version
  Push-Location (Join-Path $PSScriptRoot "..\\go")
  try {
    go run .\\core\\cmd\\core
  } finally {
    Pop-Location
  }
}

function Invoke-DevPy {
  $env:SERVICE = $PyService
  $env:ENV = $Env
  $env:PORT = "$PyPort"
  $env:VERSION = $Version
  Push-Location (Join-Path $PSScriptRoot "..\\python\\services\\app")
  try {
    python -m uvicorn main:app --host 0.0.0.0 --port $PyPort
  } finally {
    Pop-Location
  }
}

function Invoke-DevConsumer {
  $env:SERVICE_NAME = "task-events-consumer"
  $env:ENV = $Env
  $env:VERSION = $Version
  Push-Location (Join-Path $PSScriptRoot "..\\go")
  try {
    go run .\\api\\cmd\\consumer
  } finally {
    Pop-Location
  }
}

function Invoke-DevWorker {
  $env:SERVICE_NAME = "outbox-worker"
  $env:ENV = $Env
  $env:VERSION = $Version
  Push-Location (Join-Path $PSScriptRoot "..\\go")
  try {
    go run .\\api\\cmd\\worker
  } finally {
    Pop-Location
  }
}

function Invoke-DevCongestion {
  $env:SERVICE_NAME = "congestion-worker"
  $env:ENV = $Env
  $env:VERSION = $Version
  Push-Location (Join-Path $PSScriptRoot "..\\go")
  try {
    go run .\\api\\cmd\\congestion-worker
  } finally {
    Pop-Location
  }
}

switch ($Target) {
  "api" { Invoke-DevApi }
  "core" { Invoke-DevCore }
  "py" { Invoke-DevPy }
  "consumer" { Invoke-DevConsumer }
  "worker" { Invoke-DevWorker }
  "congestion" { Invoke-DevCongestion }
}
