.PHONY: help dev-api dev-core dev-py dev-consumer dev-worker dev-congestion migrate-up

ENV ?= dev
VERSION ?= dev

API_PORT ?= 8080
CORE_PORT ?= 8081
PY_PORT ?= 8000
PY_SERVICE ?= app

help:
	@echo "Targets:"
	@echo "  make dev-api   (default ENV=dev PORT=$(API_PORT))"
	@echo "  make dev-core  (default ENV=dev PORT=$(CORE_PORT))"
	@echo "  make dev-py    (default ENV=dev PORT=$(PY_PORT))"
	@echo "  make dev-consumer (task.events consumer)"
	@echo "  make dev-worker (outbox asynq worker)"
	@echo "  make dev-congestion (congestion worker)"
	@echo "  make migrate-up (uses DATABASE_URL)"
	@echo ""
	@echo "Override with: ENV=prod VERSION=... API_PORT=... CORE_PORT=... PY_PORT=... PY_SERVICE=..."

dev-api:
	@cd go && SERVICE_NAME=api ENV=$(ENV) PORT=$(API_PORT) VERSION=$(VERSION) go run ./api/cmd/api

dev-core:
	@cd go && SERVICE_NAME=core ENV=$(ENV) PORT=$(CORE_PORT) VERSION=$(VERSION) go run ./core/cmd/core

dev-py:
	@cd python/services/app && SERVICE=$(PY_SERVICE) ENV=$(ENV) PORT=$(PY_PORT) VERSION=$(VERSION) python -m uvicorn main:app --host 0.0.0.0 --port $(PY_PORT)

dev-consumer:
	@cd go && SERVICE_NAME=task-events-consumer ENV=$(ENV) VERSION=$(VERSION) go run ./api/cmd/consumer

dev-worker:
	@cd go && SERVICE_NAME=outbox-worker ENV=$(ENV) VERSION=$(VERSION) go run ./api/cmd/worker

dev-congestion:
	@cd go && SERVICE_NAME=congestion-worker ENV=$(ENV) VERSION=$(VERSION) go run ./api/cmd/congestion-worker

migrate-up:
	@powershell -File scripts/migrate.ps1
