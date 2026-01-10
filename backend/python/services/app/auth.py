import json
import time
import urllib.request
from dataclasses import dataclass
from typing import Any, Dict, Optional, Tuple

import jwt


class AuthError(Exception):
    def __init__(self, message: str):
        super().__init__(message)
        self.message = message


@dataclass(frozen=True)
class OIDCConfig:
    issuer: str
    audience: str
    jwks_url: str
    jwks_cache_ttl_seconds: int
    jwt_clock_skew_seconds: int


def _default_jwks_url(issuer: str) -> str:
    issuer = issuer.rstrip("/")
    return f"{issuer}/.well-known/jwks.json"


class JWKSCache:
    def __init__(self, jwks_url: str, ttl_seconds: int):
        self._jwks_url = jwks_url
        self._ttl_seconds = ttl_seconds
        self._keys_by_kid: Dict[str, Any] = {}
        self._expires_at_monotonic: float = 0.0

    def _fetch_jwks(self) -> Dict[str, Any]:
        req = urllib.request.Request(self._jwks_url, headers={"Accept": "application/json"})
        with urllib.request.urlopen(req, timeout=5) as resp:  # noqa: S310
            data = resp.read()
        return json.loads(data.decode("utf-8"))

    def _refresh(self) -> None:
        jwks = self._fetch_jwks()
        keys = jwks.get("keys")
        if not isinstance(keys, list):
            raise AuthError("invalid JWKS: missing keys")

        keys_by_kid: Dict[str, Any] = {}
        for k in keys:
            if not isinstance(k, dict):
                continue
            kid = str(k.get("kid") or "").strip()
            if not kid:
                continue
            try:
                kty = str(k.get("kty") or "").strip().upper()
                if kty == "RSA":
                    keys_by_kid[kid] = jwt.algorithms.RSAAlgorithm.from_jwk(json.dumps(k))
                elif kty == "EC":
                    keys_by_kid[kid] = jwt.algorithms.ECAlgorithm.from_jwk(json.dumps(k))
                else:
                    continue
            except Exception:  # noqa: BLE001
                continue

        if not keys_by_kid:
            raise AuthError("invalid JWKS: no usable keys")

        self._keys_by_kid = keys_by_kid
        self._expires_at_monotonic = time.monotonic() + max(self._ttl_seconds, 1)

    def get_key(self, kid: str) -> Any:
        now = time.monotonic()
        if now >= self._expires_at_monotonic:
            try:
                self._refresh()
            except Exception:
                key = self._keys_by_kid.get(kid)
                if key is not None:
                    return key
                raise

        key = self._keys_by_kid.get(kid)
        if key is not None:
            return key

        # Likely key rotation; do a one-time refresh on cache miss.
        self._refresh()
        key = self._keys_by_kid.get(kid)
        if key is None:
            raise AuthError("unknown kid")
        return key


class JWTVerifier:
    def __init__(self, cfg: OIDCConfig):
        self._cfg = cfg
        self._jwks_cache = JWKSCache(cfg.jwks_url, cfg.jwks_cache_ttl_seconds)

    @classmethod
    def from_settings(cls, settings: Any) -> Optional["JWTVerifier"]:
        issuer = (getattr(settings, "OIDC_ISSUER", None) or "").strip()
        audience = (getattr(settings, "OIDC_AUDIENCE", None) or "").strip()
        if not issuer or not audience:
            return None

        jwks_url = (getattr(settings, "OIDC_JWKS_URL", None) or "").strip() or _default_jwks_url(issuer)
        ttl = int(getattr(settings, "JWKS_CACHE_TTL_SECONDS", 300))
        skew = int(getattr(settings, "JWT_CLOCK_SKEW_SECONDS", 60))
        return cls(
            OIDCConfig(
                issuer=issuer,
                audience=audience,
                jwks_url=jwks_url,
                jwks_cache_ttl_seconds=ttl,
                jwt_clock_skew_seconds=skew,
            )
        )

    def verify(self, token: str) -> Tuple[Dict[str, Any], str]:
        try:
            header = jwt.get_unverified_header(token)
        except Exception as exc:  # noqa: BLE001
            raise AuthError("invalid token header") from exc

        alg = str(header.get("alg") or "").strip()
        kid = str(header.get("kid") or "").strip()
        if not alg or not kid:
            raise AuthError("missing alg/kid")

        allowed_algs = {"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}
        if alg not in allowed_algs:
            raise AuthError("unsupported alg")

        key = self._jwks_cache.get_key(kid)

        try:
            claims = jwt.decode(
                token,
                key=key,
                algorithms=[alg],
                issuer=self._cfg.issuer,
                audience=self._cfg.audience,
                leeway=self._cfg.jwt_clock_skew_seconds,
                options={"require": ["exp", "nbf", "iss", "aud", "sub"]},
            )
        except jwt.ExpiredSignatureError as exc:
            raise AuthError("token expired") from exc
        except jwt.InvalidTokenError as exc:
            raise AuthError("invalid token") from exc

        subject = str(claims.get("sub") or "").strip()
        if not subject:
            raise AuthError("missing sub")

        return claims, subject
