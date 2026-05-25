#!/usr/bin/env bash
#
# Demo helpers — sourceable from inside the `cli` container.
#
# The shim's AS OF expression accepts string literals only (the
# regex captures '<ts>' between quotes), so the demo can't use
# MySQL user variables across `_flashback` queries. These bash
# helpers capture timestamps into shell vars and inline them into
# the SQL via `mysql -e`, making the demo flow scriptable.
#
# Usage (inside `make cli`):
#
#   source /etc/bintrail/demo.sh
#   T0=$(now);  echo "T0 = $T0"
#   change_qty 5 2;   T1=$(now)
#   change_status 5 shipped;   T2=$(now)
#   delete_order 5;   T3=$(now)
#   flashback 5 "$T1"      # state after first change
#   flashback 5 "$T2"      # state after second change
#   diff_pk 5 "$T0" "$(now)"
#   recover_pk 5 /tmp/recover-5.sql
#   apply_sql /tmp/recover-5.sql
#   show_pk 5
#
# Every helper prints what it ran (set -x style) so the demo
# audience sees the actual SQL.

# Note: no `set -u` because this file is sourced into an interactive
# shell, where strict-unset trips on every bash tab-completion / PS1
# rebuild. The helpers use ${VAR:-default} for safety instead.

# ───────────────────────── Connection defaults ─────────────────────────

PXY_HOST="${PXY_HOST:-proxysql}"
PXY_PORT="${PXY_PORT:-6033}"
APP_USER="${APP_USER:-app}"
APP_PASS="${APP_PASS:-apppw}"
APP_DB="${APP_DB:-appdb}"
IDX_DSN="${IDX_DSN:-root:demoroot@tcp(mysql:3306)/bintrail_index}"

mysql_app() {
    mysql -h "$PXY_HOST" -P "$PXY_PORT" -u "$APP_USER" -p"$APP_PASS" "$APP_DB" "$@"
}

# ───────────────────────── Timestamp capture ─────────────────────────

# now → echo the current UTC time as 'YYYY-MM-DD HH:MM:SS' (DATETIME
# resolution; we sleep 2s between events to guarantee distinct values).
now() {
    mysql_app -BNe "SELECT UTC_TIMESTAMP()"
}

# ───────────────────────── Mutations ─────────────────────────

show_pk() {
    local pk=$1
    echo "--- SELECT * FROM orders WHERE id = $pk;"
    mysql_app -e "SELECT id, customer_id, product, qty, price_cents, status, updated_at FROM orders WHERE id = $pk\\G"
}

change_qty() {
    local pk=$1
    local qty=$2
    echo "--- UPDATE orders SET qty = $qty WHERE id = $pk;"
    mysql_app -e "UPDATE orders SET qty = $qty WHERE id = $pk;"
    sleep 2
}

change_status() {
    local pk=$1
    local status=$2
    echo "--- UPDATE orders SET status = '$status' WHERE id = $pk;"
    mysql_app -e "UPDATE orders SET status = '$status' WHERE id = $pk;"
    sleep 2
}

delete_order() {
    local pk=$1
    echo "--- DELETE FROM orders WHERE id = $pk;"
    mysql_app -e "DELETE FROM orders WHERE id = $pk;"
    sleep 2
}

# ───────────────────────── Time-travel queries ─────────────────────────

flashback() {
    local pk=$1
    local at=$2
    echo "--- SELECT * FROM _flashback.orders AS OF '$at' WHERE id = $pk;"
    mysql_app -e "SELECT * FROM _flashback.orders AS OF '$at' WHERE id = $pk\\G"
}

snapshot_at() {
    local pk=$1
    local at=$2
    echo "--- SELECT * FROM _snapshot.orders AS OF '$at' WHERE id = $pk;"
    mysql_app -e "SELECT * FROM _snapshot.orders AS OF '$at' WHERE id = $pk\\G"
}

diff_pk() {
    local pk=$1
    local since=$2
    local until=$3
    # _diff's parser requires `SELECT *` (no column list); the helper
    # asks for everything and the vertical-print (\G) keeps it readable.
    echo "--- SELECT * FROM _diff.orders BETWEEN '$since' AND '$until' WHERE id = $pk;"
    mysql_app -e "SELECT * FROM _diff.orders BETWEEN '$since' AND '$until' WHERE id = $pk\\G"
}

# Row count at an instant. The shim's AS OF parser doesn't accept
# COUNT(*) (only `*` or a bare identifier list), so we ask for `id`
# and count client-side. -BN strips headers and borders.
flashback_full_count() {
    local at=$1
    local count
    count=$(mysql_app -BNe "SELECT id FROM _flashback.orders AS OF '$at';" | wc -l)
    echo "Rows at $at: $count"
}

# ───────────────────────── Recovery ─────────────────────────

# recover_pk <pk> <output_sql_path>
#
# Generates reversal SQL for the given PK. By default recovers all
# captured events for that PK. Pass --since/--until via REC_SINCE /
# REC_UNTIL env vars to constrain the window.
recover_pk() {
    local pk=$1
    local out=$2
    local extra_args=()
    [[ -n "${REC_SINCE:-}" ]] && extra_args+=(--since "$REC_SINCE")
    [[ -n "${REC_UNTIL:-}" ]] && extra_args+=(--until "$REC_UNTIL")
    echo "--- bintrail recover --pk=$pk --output=$out ${extra_args[*]:-}"
    bintrail recover \
        --index-dsn="$IDX_DSN" \
        --schema=appdb --table=orders \
        --pk="$pk" \
        --output="$out" \
        "${extra_args[@]}"
    echo "--- generated $(wc -l < "$out") lines of reversal SQL:"
    cat "$out"
}

# recover_window <since> <until> <output_sql_path>
#
# Recover everything in a time window (no PK filter). Use for the
# "what happened in the last N seconds" demo.
recover_window() {
    local since=$1
    local until=$2
    local out=$3
    echo "--- bintrail recover --since='$since' --until='$until' --output=$out"
    bintrail recover \
        --index-dsn="$IDX_DSN" \
        --schema=appdb --table=orders \
        --since="$since" --until="$until" \
        --output="$out"
    echo "--- generated $(wc -l < "$out") lines of reversal SQL"
}

apply_sql() {
    local path=$1
    echo "--- mysql appdb < $path"
    mysql_app < "$path"
}

# ───────────────────────── Index inspection ─────────────────────────

bt_status() {
    bintrail status --index-dsn="$IDX_DSN"
}

bt_query_pk() {
    local pk=$1
    bintrail query \
        --index-dsn="$IDX_DSN" \
        --schema=appdb --table=orders \
        --pk="$pk" --order=ASC
}

echo "demo helpers loaded — try: now, show_pk 5, change_qty 5 2, flashback 5 \"\$T1\""
