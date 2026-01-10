from __future__ import annotations

from typing import Optional

import psycopg
from psycopg_pool import ConnectionPool


def create_pool(dsn: Optional[str], min_conns: int, max_conns: int) -> Optional[ConnectionPool]:
    if not dsn:
        return None
    return ConnectionPool(conninfo=dsn, min_size=min_conns, max_size=max_conns, open=True)


def ping(pool: Optional[ConnectionPool]) -> None:
    if pool is None:
        raise RuntimeError("db pool not configured")
    with pool.connection() as conn:
        with conn.cursor() as cur:
            cur.execute("SELECT 1")
            cur.fetchone()
