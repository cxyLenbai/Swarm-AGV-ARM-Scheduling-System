# Python service (FastAPI)

## Run

From this directory:

```bash
pip install -r requirements.txt
uvicorn main:app --host 0.0.0.0 --port 8000
```

Environment variables (optional):

- `SERVICE` (default: `app`)
- `ENV` (default: `dev`)
- `PORT` (default: `8000`)
- `VERSION` (default: `1.0.0`)

## Endpoints

- `GET /healthz` returns `service/env/port/version`
