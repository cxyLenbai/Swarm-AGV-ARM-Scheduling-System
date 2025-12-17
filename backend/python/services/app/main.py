import json
import logging
from fastapi import FastAPI

try:
    from .settings import settings
except ImportError:  # Allows `uvicorn main:app` when cwd is this folder
    from settings import settings



class JsonFormatter(logging.Formatter):
    def format(self, record):
        log_record = {
            "level": record.levelname,
            "message": record.getMessage(),
            "timestamp": self.formatTime(record, self.datefmt),
        }
        for key in ("service", "env", "port", "version"):
            if hasattr(record, key):
                log_record[key] = getattr(record, key)
        return json.dumps(log_record)
logger = logging.getLogger("app")
logger.setLevel(logging.INFO)
handler = logging.StreamHandler()
handler.setFormatter(JsonFormatter(datefmt="%Y-%m-%d %H:%M:%S"))
logger.addHandler(handler)
logger.propagate = False  # 避免重复输出
app = FastAPI(
    title = settings.SERVICE,
    version = settings.VERSION,
    description = f"Service running in {settings.ENV} environment"
)


@app.get("/healthz")
def healthz():
    return {
        "service": settings.SERVICE,
        "env": settings.ENV,
        "port": settings.PORT,
        "version": settings.VERSION,
        "status": "ok",
    }


@app.on_event("startup")
async def log_startup():
    logger.info(
        "service_start",
        extra={
            "service": settings.SERVICE,
            "env": settings.ENV,
            "port": settings.PORT,
            "version": settings.VERSION,
        },
    )


def main():
    import uvicorn

    uvicorn.run(
        "main:app",
        host="0.0.0.0",
        port=settings.PORT,
        reload=False,
    )


if __name__ == "__main__":
    main()
