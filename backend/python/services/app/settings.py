import json
import os
from dataclasses import dataclass
from pathlib import Path
from typing import Dict, List, Optional, Tuple


@dataclass(frozen=True)
class AppSettings:
    ENV: str
    SERVICE_NAME: str
    HTTP_PORT: int
    LOG_LEVEL: str
    CONFIG_PATH: Optional[str]
    REQUEST_TIMEOUT_MS: int
    VERSION: Optional[str]
    OIDC_ISSUER: Optional[str]
    OIDC_AUDIENCE: Optional[str]
    OIDC_JWKS_URL: Optional[str]
    JWKS_CACHE_TTL_SECONDS: int
    JWT_CLOCK_SKEW_SECONDS: int
    DATABASE_URL: Optional[str]
    DB_MAX_CONNS: int
    DB_MIN_CONNS: int
    DB_CONN_MAX_IDLE_SECONDS: int
    DB_CONN_MAX_LIFETIME_SECONDS: int
    AUDIT_ENABLED: bool
    REDIS_ADDR: Optional[str]
    REDIS_PASSWORD: Optional[str]
    REDIS_DB: int
    INFLUX_URL: Optional[str]
    INFLUX_TOKEN: Optional[str]
    INFLUX_ORG: Optional[str]
    INFLUX_BUCKET: Optional[str]


def _find_repo_root(start: Path) -> Optional[Path]:
    for candidate in (start, *start.parents):
        if (candidate / "backend" / "configs").is_dir():
            return candidate
    return None


def _default_config_path(env_name: str) -> Path:
    base = _find_repo_root(Path(__file__).resolve()) or Path.cwd()
    return base / "backend" / "configs" / f"{env_name}.json"


def _load_config_file(path: Path) -> Tuple[Dict, List[Dict]]:
    try:
        raw = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        return {}, [{"field": "CONFIG_PATH", "message": "config file not found"}]
    except Exception as exc:  # noqa: BLE001
        return {}, [{"field": "CONFIG_PATH", "message": f"invalid config file: {exc}"}]

    if not isinstance(raw, dict):
        return {}, [{"field": "CONFIG_PATH", "message": "config file must be a JSON object"}]

    normalized = {str(k).upper(): v for k, v in raw.items()}
    return normalized, []


def load_settings(service_name_default: str = "services", http_port_default: int = 8000) -> Tuple[AppSettings, List[Dict]]:
    problems: List[Dict] = []

    env_name = os.getenv("ENV", "").strip()
    explicit_config_path = os.getenv("CONFIG_PATH", "").strip()

    file_data: Dict = {}
    if explicit_config_path:
        file_data, file_problems = _load_config_file(Path(explicit_config_path))
        problems.extend(file_problems)
    elif env_name:
        default_path = _default_config_path(env_name)
        if default_path.exists():
            file_data, file_problems = _load_config_file(default_path)
            problems.extend(file_problems)

    if not env_name:
        env_name = str(file_data.get("ENV") or "").strip()

    service_name = str(file_data.get("SERVICE_NAME") or "").strip() or service_name_default
    http_port = int(file_data.get("HTTP_PORT") or http_port_default)
    log_level = str(file_data.get("LOG_LEVEL") or "").strip() or "info"
    request_timeout_ms = int(file_data.get("REQUEST_TIMEOUT_MS") or 30000)
    version = str(file_data.get("VERSION") or "").strip() or os.getenv("VERSION", "").strip() or None

    oidc_issuer = str(file_data.get("OIDC_ISSUER") or "").strip() or None
    oidc_audience = str(file_data.get("OIDC_AUDIENCE") or "").strip() or None
    oidc_jwks_url = str(file_data.get("OIDC_JWKS_URL") or "").strip() or None
    jwks_cache_ttl_seconds = int(file_data.get("JWKS_CACHE_TTL_SECONDS") or 300)
    jwt_clock_skew_seconds = int(file_data.get("JWT_CLOCK_SKEW_SECONDS") or 60)
    database_url = str(file_data.get("DATABASE_URL") or "").strip() or None
    db_max_conns = int(file_data.get("DB_MAX_CONNS") or 10)
    db_min_conns = int(file_data.get("DB_MIN_CONNS") or 1)
    db_conn_max_idle_seconds = int(file_data.get("DB_CONN_MAX_IDLE_SECONDS") or 300)
    db_conn_max_lifetime_seconds = int(file_data.get("DB_CONN_MAX_LIFETIME_SECONDS") or 1800)
    audit_enabled = str(file_data.get("AUDIT_ENABLED") or "").strip().lower() in ("1", "true", "yes", "y")
    redis_addr = str(file_data.get("REDIS_ADDR") or "").strip() or None
    redis_password = str(file_data.get("REDIS_PASSWORD") or "").strip() or None
    redis_db = int(file_data.get("REDIS_DB") or 0)
    influx_url = str(file_data.get("INFLUX_URL") or "").strip() or None
    influx_token = str(file_data.get("INFLUX_TOKEN") or "").strip() or None
    influx_org = str(file_data.get("INFLUX_ORG") or "").strip() or None
    influx_bucket = str(file_data.get("INFLUX_BUCKET") or "").strip() or None

    if v := os.getenv("SERVICE_NAME", "").strip():
        service_name = v
    elif v := os.getenv("SERVICE", "").strip():
        service_name = v

    if v := os.getenv("HTTP_PORT", "").strip():
        try:
            http_port = int(v)
        except ValueError:
            problems.append({"field": "HTTP_PORT", "message": "HTTP_PORT must be an integer"})
    elif v := os.getenv("PORT", "").strip():
        try:
            http_port = int(v)
        except ValueError:
            problems.append({"field": "HTTP_PORT", "message": "HTTP_PORT must be an integer"})

    if v := os.getenv("LOG_LEVEL", "").strip():
        log_level = v

    if v := os.getenv("REQUEST_TIMEOUT_MS", "").strip():
        try:
            request_timeout_ms = int(v)
        except ValueError:
            problems.append({"field": "REQUEST_TIMEOUT_MS", "message": "REQUEST_TIMEOUT_MS must be an integer"})

    if v := os.getenv("OIDC_ISSUER", "").strip():
        oidc_issuer = v
    if v := os.getenv("OIDC_AUDIENCE", "").strip():
        oidc_audience = v
    if v := os.getenv("OIDC_JWKS_URL", "").strip():
        oidc_jwks_url = v
    if v := os.getenv("JWKS_CACHE_TTL_SECONDS", "").strip():
        try:
            jwks_cache_ttl_seconds = int(v)
        except ValueError:
            problems.append({"field": "JWKS_CACHE_TTL_SECONDS", "message": "JWKS_CACHE_TTL_SECONDS must be an integer"})
    if v := os.getenv("JWT_CLOCK_SKEW_SECONDS", "").strip():
        try:
            jwt_clock_skew_seconds = int(v)
        except ValueError:
            problems.append({"field": "JWT_CLOCK_SKEW_SECONDS", "message": "JWT_CLOCK_SKEW_SECONDS must be an integer"})
    if v := os.getenv("DATABASE_URL", "").strip():
        database_url = v
    if v := os.getenv("DB_MAX_CONNS", "").strip():
        try:
            db_max_conns = int(v)
        except ValueError:
            problems.append({"field": "DB_MAX_CONNS", "message": "DB_MAX_CONNS must be an integer"})
    if v := os.getenv("DB_MIN_CONNS", "").strip():
        try:
            db_min_conns = int(v)
        except ValueError:
            problems.append({"field": "DB_MIN_CONNS", "message": "DB_MIN_CONNS must be an integer"})
    if v := os.getenv("DB_CONN_MAX_IDLE_SECONDS", "").strip():
        try:
            db_conn_max_idle_seconds = int(v)
        except ValueError:
            problems.append({"field": "DB_CONN_MAX_IDLE_SECONDS", "message": "DB_CONN_MAX_IDLE_SECONDS must be an integer"})
    if v := os.getenv("DB_CONN_MAX_LIFETIME_SECONDS", "").strip():
        try:
            db_conn_max_lifetime_seconds = int(v)
        except ValueError:
            problems.append({"field": "DB_CONN_MAX_LIFETIME_SECONDS", "message": "DB_CONN_MAX_LIFETIME_SECONDS must be an integer"})
    if v := os.getenv("AUDIT_ENABLED", "").strip():
        audit_enabled = v.strip().lower() in ("1", "true", "yes", "y")
    if v := os.getenv("REDIS_ADDR", "").strip():
        redis_addr = v
    if v := os.getenv("REDIS_PASSWORD", "").strip():
        redis_password = v
    if v := os.getenv("REDIS_DB", "").strip():
        try:
            redis_db = int(v)
        except ValueError:
            problems.append({"field": "REDIS_DB", "message": "REDIS_DB must be an integer"})
    if v := os.getenv("INFLUX_URL", "").strip():
        influx_url = v
    if v := os.getenv("INFLUX_TOKEN", "").strip():
        influx_token = v
    if v := os.getenv("INFLUX_ORG", "").strip():
        influx_org = v
    if v := os.getenv("INFLUX_BUCKET", "").strip():
        influx_bucket = v

    if not env_name:
        problems.append({"field": "ENV", "message": "ENV is required"})
        env_name = "dev"

    if not (1 <= http_port <= 65535):
        problems.append({"field": "HTTP_PORT", "message": "HTTP_PORT must be 1-65535"})
        http_port = http_port_default

    if request_timeout_ms <= 0:
        problems.append({"field": "REQUEST_TIMEOUT_MS", "message": "REQUEST_TIMEOUT_MS must be > 0"})
        request_timeout_ms = 30000

    if jwks_cache_ttl_seconds <= 0:
        problems.append({"field": "JWKS_CACHE_TTL_SECONDS", "message": "JWKS_CACHE_TTL_SECONDS must be > 0"})
        jwks_cache_ttl_seconds = 300
    if jwt_clock_skew_seconds < 0:
        problems.append({"field": "JWT_CLOCK_SKEW_SECONDS", "message": "JWT_CLOCK_SKEW_SECONDS must be >= 0"})
        jwt_clock_skew_seconds = 60
    if db_max_conns <= 0:
        problems.append({"field": "DB_MAX_CONNS", "message": "DB_MAX_CONNS must be > 0"})
        db_max_conns = 10
    if db_min_conns < 0:
        problems.append({"field": "DB_MIN_CONNS", "message": "DB_MIN_CONNS must be >= 0"})
        db_min_conns = 1
    if db_min_conns > db_max_conns:
        problems.append({"field": "DB_MIN_CONNS", "message": "DB_MIN_CONNS must be <= DB_MAX_CONNS"})
        db_min_conns = db_max_conns
    if db_conn_max_idle_seconds <= 0:
        problems.append({"field": "DB_CONN_MAX_IDLE_SECONDS", "message": "DB_CONN_MAX_IDLE_SECONDS must be > 0"})
        db_conn_max_idle_seconds = 300
    if db_conn_max_lifetime_seconds <= 0:
        problems.append({"field": "DB_CONN_MAX_LIFETIME_SECONDS", "message": "DB_CONN_MAX_LIFETIME_SECONDS must be > 0"})
        db_conn_max_lifetime_seconds = 1800
    if redis_db < 0:
        problems.append({"field": "REDIS_DB", "message": "REDIS_DB must be >= 0"})
        redis_db = 0

    return (
        AppSettings(
            ENV=env_name,
            SERVICE_NAME=service_name,
            HTTP_PORT=http_port,
            LOG_LEVEL=log_level,
            CONFIG_PATH=explicit_config_path or None,
            REQUEST_TIMEOUT_MS=request_timeout_ms,
            VERSION=version,
            OIDC_ISSUER=oidc_issuer,
            OIDC_AUDIENCE=oidc_audience,
            OIDC_JWKS_URL=oidc_jwks_url,
            JWKS_CACHE_TTL_SECONDS=jwks_cache_ttl_seconds,
            JWT_CLOCK_SKEW_SECONDS=jwt_clock_skew_seconds,
            DATABASE_URL=database_url,
            DB_MAX_CONNS=db_max_conns,
            DB_MIN_CONNS=db_min_conns,
            DB_CONN_MAX_IDLE_SECONDS=db_conn_max_idle_seconds,
            DB_CONN_MAX_LIFETIME_SECONDS=db_conn_max_lifetime_seconds,
            AUDIT_ENABLED=audit_enabled,
            REDIS_ADDR=redis_addr,
            REDIS_PASSWORD=redis_password,
            REDIS_DB=redis_db,
            INFLUX_URL=influx_url,
            INFLUX_TOKEN=influx_token,
            INFLUX_ORG=influx_org,
            INFLUX_BUCKET=influx_bucket,
        ),
        problems,
    )


settings, config_problems = load_settings()
