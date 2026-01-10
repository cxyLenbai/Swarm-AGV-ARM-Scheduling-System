import json
import logging
import uuid
from datetime import datetime, timezone
import asyncio
from typing import Any, Dict, Optional, Tuple
from fastapi import FastAPI, Request
from fastapi.responses import JSONResponse
from fastapi.exceptions import RequestValidationError
from starlette.exceptions import HTTPException as StarletteHTTPException

try:
    from .settings import config_problems, settings
    from .auth import AuthError, JWTVerifier
    from .db import create_pool, ping
except ImportError:  # Allows `uvicorn main:app` when cwd is this folder
    from settings import config_problems, settings
    from auth import AuthError, JWTVerifier
    from db import create_pool, ping



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
            "tenant_id",
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
db_pool = create_pool(settings.DATABASE_URL, settings.DB_MIN_CONNS, settings.DB_MAX_CONNS)


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


def is_write_method(method: str) -> bool:
    return method in ("POST", "PUT", "PATCH", "DELETE")


def should_audit(path: str, method: str, status_code: int) -> bool:
    if status_code == 401:
        return True
    if is_write_method(method):
        return True
    return "/robots" in path or "/tasks" in path


def audit_action(method: str, status_code: int) -> str:
    if status_code == 401:
        return "auth_failed"
    if method == "POST":
        return "create"
    if method in ("PUT", "PATCH"):
        return "update"
    if method == "DELETE":
        return "delete"
    return "read"


def resource_from_path(path: str) -> Tuple[Optional[str], Optional[str]]:
    parts = [p for p in path.strip("/").split("/") if p]
    if len(parts) >= 3 and parts[0] == "api" and parts[1] == "v1":
        resource = parts[2]
        if resource in ("robots", "tasks"):
            resource_id = parts[3] if len(parts) >= 4 else None
            return resource, resource_id
    return None, None


async def fetch_tenant_by_slug(slug: str) -> Optional[Tuple[str, str, str]]:
    if db_pool is None:
        return None

    def _query() -> Optional[Tuple[str, str, str]]:
        with db_pool.connection() as conn:
            with conn.cursor() as cur:
                cur.execute(
                    "SELECT tenant_id, slug, name FROM tenants WHERE slug = %s",
                    (slug,),
                )
                row = cur.fetchone()
                if row:
                    return str(row[0]), str(row[1]), str(row[2])
        return None

    return await asyncio.to_thread(_query)


async def write_audit_log(entry: Dict[str, Any]) -> None:
    if db_pool is None:
        return

    def _write() -> None:
        with db_pool.connection() as conn:
            with conn.cursor() as cur:
                cur.execute(
                    """
                    INSERT INTO audit_logs (
                        occurred_at, tenant_id, actor_user_id, subject, action,
                        resource_type, resource_id, request_id, method, path,
                        status_code, duration_ms, client_ip, user_agent, details
                    ) VALUES (
                        %s, %s, %s, %s, %s,
                        %s, %s, %s, %s, %s,
                        %s, %s, %s, %s, %s
                    )
                    """,
                    (
                        entry["occurred_at"],
                        entry["tenant_id"],
                        entry.get("actor_user_id"),
                        entry.get("subject"),
                        entry["action"],
                        entry.get("resource_type"),
                        entry.get("resource_id"),
                        entry.get("request_id"),
                        entry.get("method"),
                        entry.get("path"),
                        entry.get("status_code"),
                        entry.get("duration_ms"),
                        entry.get("client_ip"),
                        entry.get("user_agent"),
                        json.dumps(entry.get("details") or {}),
                    ),
                )
                conn.commit()

    await asyncio.to_thread(_write)


async def write_audit_log_safe(entry: Dict[str, Any], request_id: str) -> None:
    try:
        await write_audit_log(entry)
    except Exception as exc:  # noqa: BLE001
        logger.warning(
            "audit_write_failed",
            extra={
                "msg_text": "audit write failed",
                "service": settings.SERVICE_NAME,
                "env": settings.ENV,
                "version": settings.VERSION,
                "request_id": request_id,
                "error_code": "INTERNAL_ERROR",
                "error": repr(exc),
            },
        )


@app.middleware("http")
async def base_middleware(request: Request, call_next):
    request_id = (request.headers.get("X-Request-ID") or "").strip()
    if not request_id:
        request_id = uuid.uuid4().hex

    request.state.request_id = request_id
    start = datetime.now(tz=timezone.utc)

    try:
        timeout_s = max(settings.REQUEST_TIMEOUT_MS, 1) / 1000.0
        tenant_id = (request.headers.get("X-Tenant-ID") or "").strip()
        tenant_slug = (request.headers.get("X-Tenant-Slug") or "").strip()
        if not is_public_path(request.url.path) and jwt_verifier is None:
            response = JSONResponse(
                status_code=503,
                content=error_response("FAILED_PRECONDITION", "auth verifier not configured", request_id),
            )
        elif jwt_verifier and not is_public_path(request.url.path):
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
                    if not tenant_id and not tenant_slug:
                        response = JSONResponse(
                            status_code=400,
                            content=error_response("INVALID_ARGUMENT", "missing tenant header", request_id),
                        )
                    if tenant_slug:
                        tenant_record = await fetch_tenant_by_slug(tenant_slug)
                        if tenant_record is None:
                            response = JSONResponse(
                                status_code=404,
                                content=error_response("NOT_FOUND", "tenant not found", request_id),
                            )
                        else:
                            if tenant_id and tenant_record[0] != tenant_id:
                                response = JSONResponse(
                                    status_code=403,
                                    content=error_response("FORBIDDEN", "tenant mismatch", request_id),
                                )
                            tenant_id = tenant_record[0]
                            request.state.tenant_slug = tenant_record[1]
                            request.state.tenant_name = tenant_record[2]
                    if tenant_id:
                        claim_tenant_id = str((claims or {}).get("tenant_id") or "").strip()
                        if claim_tenant_id and claim_tenant_id != tenant_id:
                            response = JSONResponse(
                                status_code=403,
                                content=error_response("FORBIDDEN", "tenant claim mismatch", request_id),
                            )
                        tenants_claim = (claims or {}).get("tenants")
                        if tenants_claim:
                            allowed = {str(t).strip() for t in (tenants_claim if isinstance(tenants_claim, list) else str(tenants_claim).split())}
                            if tenant_id not in allowed:
                                response = JSONResponse(
                                    status_code=403,
                                    content=error_response("FORBIDDEN", "tenant not allowed", request_id),
                                )
                    if tenant_id:
                        request.state.tenant_id = tenant_id
                    if "response" not in locals():
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
        log_extra = {
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
        }
        tenant_id = getattr(request.state, "tenant_id", None)
        if tenant_id:
            log_extra["tenant_id"] = tenant_id
        logger.info(
            "http_request",
            extra=log_extra,
        )

    if settings.AUDIT_ENABLED and not is_public_path(request.url.path):
        status_code = getattr(response, "status_code", 200)
        if should_audit(request.url.path, request.method, status_code):
            tenant_id = getattr(request.state, "tenant_id", None) or (request.headers.get("X-Tenant-ID") or "").strip()
            if tenant_id:
                resource_type, resource_id = resource_from_path(request.url.path)
                entry = {
                    "occurred_at": datetime.now(tz=timezone.utc),
                    "tenant_id": tenant_id,
                    "actor_user_id": None,
                    "subject": getattr(request.state, "subject", None),
                    "action": audit_action(request.method, status_code),
                    "resource_type": resource_type,
                    "resource_id": resource_id,
                    "request_id": request_id,
                    "method": request.method,
                    "path": request.url.path,
                    "status_code": status_code,
                    "duration_ms": int((datetime.now(tz=timezone.utc) - start).total_seconds() * 1000),
                    "client_ip": client_ip(request),
                    "user_agent": request.headers.get("User-Agent"),
                    "details": {"status_code": status_code},
                }
                asyncio.create_task(write_audit_log_safe(entry, request_id))

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


@app.get("/api/v1/me")
def me(request: Request):
    claims = getattr(request.state, "jwt_claims", None)
    subject = getattr(request.state, "subject", None)
    if not subject:
        return JSONResponse(
            status_code=401,
            content=error_response("UNAUTHENTICATED", "missing auth context", get_request_id(request)),
        )
    return {
        "subject": subject,
        "email": (claims or {}).get("email"),
        "name": (claims or {}).get("name"),
        "roles": (claims or {}).get("roles"),
        "claims": claims or {},
    }


@app.get("/api/v1/tenants/current")
def current_tenant(request: Request):
    tenant_id = getattr(request.state, "tenant_id", None)
    tenant_slug = getattr(request.state, "tenant_slug", None)
    tenant_name = getattr(request.state, "tenant_name", None)
    if not tenant_id:
        return JSONResponse(
            status_code=400,
            content=error_response("INVALID_ARGUMENT", "missing tenant", get_request_id(request)),
        )
    return {
        "tenant_id": tenant_id,
        "slug": tenant_slug,
        "name": tenant_name,
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
    if not settings.DATABASE_URL:
        return JSONResponse(
            status_code=503,
            content=error_response(
                "FAILED_PRECONDITION",
                "service not ready: database not configured",
                get_request_id(request),
                details={"problem": "DATABASE_URL is required"},
            ),
        )
    try:
        ping(db_pool)
    except Exception:  # noqa: BLE001
        return JSONResponse(
            status_code=503,
            content=error_response(
                "FAILED_PRECONDITION",
                "service not ready: database unavailable",
                get_request_id(request),
                details={"problem": "db_ping_failed"},
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


@app.on_event("shutdown")
async def shutdown():
    if db_pool is not None:
        db_pool.close()


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
