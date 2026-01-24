# Backend Runbook

## Prerequisites
- PostgreSQL (with PostGIS extension available)
- Kafka
- Redis
- InfluxDB
- Optional: RMF adapter service, Python AI service

## Local setup
1. Apply database migrations:
   - `powershell -File backend/scripts/migrate.ps1`
2. Start API and core services:
   - `make dev-api`
   - `make dev-core`
3. Start background workers:
   - `make dev-worker`
   - `make dev-consumer`
   - `make dev-congestion`
4. Start Python AI service:
   - `make dev-py`

## Health checks
- `GET /healthz`
- `GET /readyz`
- `GET /metrics`

## Key endpoints
- `POST /api/v1/robots/{robot_code}/status`
- `GET /api/v1/congestion/hotspots`
- `GET /api/v1/congestion/alerts`
- `POST /api/v1/congestion/predict`
- `POST /api/v1/rmf/tasks`

## Common operations
- Ensure `KAFKA_BROKERS`, `DATABASE_URL`, `REDIS_ADDR`, and `INFLUX_*` are set.
- Enable AI by setting `AI_ENABLED=true` and `AI_SERVICE_URL`.
- Enable RMF by setting `RMF_ENABLED=true` and `RMF_API_URL`.
- Enable tracing by setting `OTEL_ENABLED=true` and `OTEL_EXPORTER_OTLP_ENDPOINT`.

## Integration tests
- Start dependencies: `docker compose -f backend/tests/integration/docker-compose.yml up -d`
- Run tests: `DATABASE_URL=... KAFKA_BROKERS=... REDIS_ADDR=... ASYNQ_REDIS_ADDR=... INFLUX_URL=... go test -tags=integration ./integration`
