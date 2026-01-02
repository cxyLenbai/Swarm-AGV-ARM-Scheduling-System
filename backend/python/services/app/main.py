import json
import logging
import uuid
from datetime import datetime, timezone
import asyncio
from typing import Any, Dict, Optional
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from fastapi.exceptions import RequestValidationError
from starlette.exceptions import HTTPException as StarletteHTTPException

try:
    from .settings import config_problems, settings
    from .auth import AuthError, JWTVerifier
except ImportError:  # Allows `uvicorn main:app` when cwd is this folder
    from settings import config_problems, settings
    from auth import AuthError, JWTVerifier



class JsonFormatter(logging.Formatter):
    def format(self, record):
        log_record: dict = {
            "ts": datetime.fromtimestamp(record.created, tz=timezone.utc).isoformat(),
            "level": record.levelname.lower(),
            "event": record.getMessage(),
            "msg": getattr(record, "msg_text", ""),
        }

        for key in (
            "service",
            "env",
            "version",
            "http_port",
            "log_level",
            "request_timeout_ms",
            "request_id",
            "method",
            "path",
            "status_code",
            "duration_ms",
            "client_ip",
            "error_code",
            "error",
            "stack",
            "details",
        ):
            if hasattr(record, key):
                log_record[key] = getattr(record, key)

        return json.dumps(log_record, ensure_ascii=False)
logger = logging.getLogger("app")
logger.setLevel(logging.INFO)
handler = logging.StreamHandler()
handler.setFormatter(JsonFormatter(datefmt="%Y-%m-%d %H:%M:%S"))
logger.addHandler(handler)
logger.propagate = False  # 避免重复输出
app = FastAPI(
    title=settings.SERVICE_NAME,
    version=settings.VERSION or "0.0.0",
    description=f"Service running in {settings.ENV} environment",
)

jwt_verifier = JWTVerifier.from_settings(settings)


def get_request_id(request: Request) -> str:
    return getattr(request.state, "request_id", "")

def is_public_path(path: str) -> bool:
    if path in ("/healthz", "/readyz", "/openapi.json"):
        return True
    if path.startswith("/docs") or path.startswith("/redoc"):
        return True
    return False

def client_ip(request: Request) -> str:
    xff = (request.headers.get("X-Forwarded-For") or "").strip()
    if xff:
        return xff.split(",")[0].strip()
    xri = (request.headers.get("X-Real-IP") or "").strip()
    if xri:
        return xri
    if request.client:
        return request.client.host
    return ""


def error_response(code: str, message: str, request_id: str, details: Optional[Dict[str, Any]] = None) -> Dict[str, Any]:
    body: Dict[str, Any] = {"error": {"code": code, "message": message, "request_id": request_id}}
    if details is not None:
        body["error"]["details"] = details
    return body


@app.middleware("http")
async def base_middleware(request: Request, call_next):
    request_id = (request.headers.get("X-Request-ID") or "").strip()
    if not request_id:
        request_id = uuid.uuid4().hex

    request.state.request_id = request_id
    start = datetime.now(tz=timezone.utc)

    try:
        timeout_s = max(settings.REQUEST_TIMEOUT_MS, 1) / 1000.0
        if jwt_verifier and not is_public_path(request.url.path):
            auth = (request.headers.get("Authorization") or "").strip()
            if not auth.lower().startswith("bearer "):
                response = JSONResponse(
                    status_code=401,
                    content=error_response("UNAUTHENTICATED", "missing bearer token", request_id),
                )
            else:
                token = auth.split(" ", 1)[1].strip()
                try:
                    claims, subject = await asyncio.to_thread(jwt_verifier.verify, token)
                except AuthError:
                    response = JSONResponse(
                        status_code=401,
                        content=error_response("UNAUTHENTICATED", "invalid token", request_id),
                    )
                else:
                    request.state.jwt_claims = claims
                    request.state.subject = subject
                    response = await asyncio.wait_for(call_next(request), timeout=timeout_s)
        else:
            response = await asyncio.wait_for(call_next(request), timeout=timeout_s)
    except asyncio.TimeoutError:
        logger.warning(
            "timeout",
            extra={
                "msg_text": "request timeout",
                "service": settings.SERVICE_NAME,
                "env": settings.ENV,
                "version": settings.VERSION,
                "request_id": request_id,
                "method": request.method,
                "path": request.url.path,
                "status_code": 504,
                "duration_ms": int((datetime.now(tz=timezone.utc) - start).total_seconds() * 1000),
                "client_ip": client_ip(request),
                "error_code": "TIMEOUT",
            },
        )
        response = JSONResponse(
            status_code=504,
            content=error_response("TIMEOUT", "request timeout", request_id),
        )

    response.headers["X-Request-ID"] = request_id

    if request.url.path != "/healthz":
        logger.info(
            "http_request",
            extra={
                "msg_text": "http request",
                "service": settings.SERVICE_NAME,
                "env": settings.ENV,
                "version": settings.VERSION,
                "request_id": request_id,
                "method": request.method,
                "path": request.url.path,
                "status_code": getattr(response, "status_code", 200),
                "duration_ms": int((datetime.now(tz=timezone.utc) - start).total_seconds() * 1000),
                "client_ip": client_ip(request),
            },
        )

    return response


def http_error_code(status_code: int) -> str:
    if status_code == 400 or status_code == 422:
        return "INVALID_ARGUMENT"
    if status_code == 401:
        return "UNAUTHENTICATED"
    if status_code == 403:
        return "FORBIDDEN"
    if status_code == 404:
        return "NOT_FOUND"
    if status_code == 409:
        return "CONFLICT"
    if status_code == 504:
        return "TIMEOUT"
    if status_code >= 500:
        return "INTERNAL_ERROR"
    return "INTERNAL_ERROR"


@app.exception_handler(StarletteHTTPException)
async def starlette_http_exception_handler(request: Request, exc: StarletteHTTPException):
    code = http_error_code(exc.status_code)
    return JSONResponse(
        status_code=exc.status_code,
        content=error_response(code, exc.detail or "http error", get_request_id(request)),
    )


@app.exception_handler(RequestValidationError)
async def request_validation_exception_handler(request: Request, exc: RequestValidationError):
    return JSONResponse(
        status_code=422,
        content=error_response(
            "INVALID_ARGUMENT",
            "validation error",
            get_request_id(request),
            details={"errors": exc.errors()},
        ),
    )


@app.exception_handler(Exception)
async def unhandled_exception_handler(request: Request, exc: Exception):
    stack = None
    if settings.ENV.lower() != "prod":
        import traceback

        stack = traceback.format_exc()

    logger.error(
        "unhandled_exception",
        extra={
            "msg_text": "unhandled exception",
            "service": settings.SERVICE_NAME,
            "env": settings.ENV,
            "version": settings.VERSION,
            "request_id": get_request_id(request),
            "error_code": "INTERNAL_ERROR",
            "error": repr(exc),
            **({"stack": stack} if stack else {}),
        },
    )
    return JSONResponse(
        status_code=500,
        content=error_response("INTERNAL_ERROR", "internal server error", get_request_id(request)),
    )


@app.get("/healthz")
def healthz():
    return {
        "service": settings.SERVICE_NAME,
        "env": settings.ENV,
        "port": settings.HTTP_PORT,
        "version": settings.VERSION,
        "status": "ok",
    }

@app.get("/readyz")
def readyz(request: Request):
    problems = config_problems
    if problems:
        return JSONResponse(
            status_code=503,
            content=error_response(
                "FAILED_PRECONDITION",
                "service not ready: invalid configuration",
                get_request_id(request),
                details={"problems": problems},
            ),
        )

    return {
        "service": settings.SERVICE_NAME,
        "env": settings.ENV,
        "port": settings.HTTP_PORT,
        "version": settings.VERSION,
        "status": "ready",
    }


@app.on_event("startup")
async def log_startup():
    logger.info(
        "service_start",
        extra={
            "msg_text": "starting service",
            "service": settings.SERVICE_NAME,
            "env": settings.ENV,
            "version": settings.VERSION,
            "http_port": settings.HTTP_PORT,
            "log_level": settings.LOG_LEVEL,
            "request_timeout_ms": settings.REQUEST_TIMEOUT_MS,
        },
    )


def main():
    import uvicorn

    uvicorn.run(
        "main:app",
        host="0.0.0.0",
        port=settings.HTTP_PORT,
        reload=False,
    )


if __name__ == "__main__":
    main()
