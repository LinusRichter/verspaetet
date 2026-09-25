#!/usr/bin/env bash
# backup-example.sh — generic Postgres backup with a monitoring heartbeat.
#
# Run from cron/systemd on the docker host. The final line writes a Prometheus
# textfile that node_exporter's textfile collector picks up, powering the
# "Backup age (days)" panel in the Grafana ops dashboard. If the backup fails,
# the timestamp is NOT written — the panel turns yellow (2d) / red (7d).
#
# Adjust the pg_dump line to your setup (container name, credentials, target).
# No credentials belong in this repo — keep them in the host's environment.
set -euo pipefail

TEXTFILE_DIR="$(dirname "$0")/textfile"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/verspaetet}"
RETAIN_DAYS=14
mkdir -p "$TEXTFILE_DIR" "$BACKUP_DIR"

STAMP="$(date +%Y-%m-%dT%H:%M:%S%z)"
OUT="$BACKUP_DIR/verspaetet-$(date +%Y%m%d-%H%M%S).sql.gz"

# Example: dump from the postgres container created by docker-compose.yml.
# docker exec <postgres-container> pg_dump -U <user> <db> | gzip > "$OUT"

if [ -s "$OUT" ]; then
    # Prune old backups.
    find "$BACKUP_DIR" -name 'verspaetet-*.sql.gz' -mtime "+$RETAIN_DAYS" -delete
    # Heartbeat for the Grafana "Backup age" panel.
    printf 'verspaetet_backup_last_success_timestamp %s\n' "$(date +%s)" \
        > "$TEXTFILE_DIR/verspaetet_backup.prom"
    echo "$STAMP backup ok: $OUT"
else
    echo "$STAMP backup FAILED (no output written — heartbeat not updated)" >&2
    exit 1
fi