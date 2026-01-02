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
        ),
        problems,
    )


settings, config_problems = load_settings()
