.PHONY: help dev-api dev-core dev-py

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
	@echo ""
	@echo "Override with: ENV=prod VERSION=... API_PORT=... CORE_PORT=... PY_PORT=... PY_SERVICE=..."

dev-api:
	@cd go && SERVICE_NAME=api ENV=$(ENV) PORT=$(API_PORT) VERSION=$(VERSION) go run ./api/cmd/api

dev-core:
	@cd go && SERVICE_NAME=core ENV=$(ENV) PORT=$(CORE_PORT) VERSION=$(VERSION) go run ./core/cmd/core

dev-py:
	@cd python/services/app && SERVICE=$(PY_SERVICE) ENV=$(ENV) PORT=$(PY_PORT) VERSION=$(VERSION) python -m uvicorn main:app --host 0.0.0.0 --port $(PY_PORT)
