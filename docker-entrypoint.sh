#!/bin/sh
# Container entrypoint: the server must run as the unprivileged app user,
# but a freshly-created named volume mounted over DATA_DIR arrives
# root-owned. When started as root, hand the state root to app (only the
# ownership fix is privileged) and drop privileges with su-exec; when
# started as any other user (e.g. `docker run --user`), just exec.
set -e

DATA_DIR="${DATA_DIR:-/data}"
mkdir -p "$DATA_DIR"

if [ "$(id -u)" = "0" ]; then
    if [ "$(stat -c %u "$DATA_DIR" 2>/dev/null || echo 0)" != "10001" ]; then
        chown -R app:app "$DATA_DIR" || true
    fi
    exec su-exec app:app "$@"
fi

exec "$@"
