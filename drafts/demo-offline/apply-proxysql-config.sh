#!/usr/bin/env bash
#
# Generate the bintrail routing SQL via `bintrail proxysql-config`
# and apply it to the ProxySQL admin interface.
#
# Runs once at stack startup as the `proxysql-setup` compose
# service. The script reads BINTRAIL_SOURCE_DSN and /etc/bintrail/
# shim.yaml to produce SQL that:
#   - registers backend MySQL in hostgroup 990 (passthrough)
#   - registers the shim sidecar at 127.0.0.1:3308 in hostgroup 991
#   - creates the 'app' user in mysql_users with the SHA1 of 'apppw'
#   - installs query rules routing _flashback / _diff / _snapshot
#     (and the /*+ DBTRAIL_AT */ hint shape) to hostgroup 991
#   - LOADs and SAVEs to disk so the config survives a restart

set -euo pipefail

: "${BINTRAIL_SOURCE_DSN:?required}"
: "${PROXYSQL_HOST:=proxysql}"
: "${PROXYSQL_ADMIN_PORT:=6032}"
: "${PROXYSQL_ADMIN_USER:=radminuser}"
: "${PROXYSQL_ADMIN_PASS:=radminpw}"

cd /etc/bintrail

echo "==> Waiting for ProxySQL admin interface at ${PROXYSQL_HOST}:${PROXYSQL_ADMIN_PORT}"
until mysql -h "${PROXYSQL_HOST}" -P "${PROXYSQL_ADMIN_PORT}" \
            -u "${PROXYSQL_ADMIN_USER}" -p"${PROXYSQL_ADMIN_PASS}" \
            -e "SELECT 1" >/dev/null 2>&1; do
    sleep 1
done
echo "==> ProxySQL admin is up"

echo "==> Generating ProxySQL routing SQL"
bintrail proxysql-config \
    --out /tmp/proxysql-setup.sql \
    --shim-config /etc/bintrail/shim.yaml \
    --force

echo "==> Applying routing SQL to ProxySQL admin"
mysql -h "${PROXYSQL_HOST}" -P "${PROXYSQL_ADMIN_PORT}" \
      -u "${PROXYSQL_ADMIN_USER}" -p"${PROXYSQL_ADMIN_PASS}" \
      < /tmp/proxysql-setup.sql

echo "==> Routing SQL applied. Verifying."
mysql -h "${PROXYSQL_HOST}" -P "${PROXYSQL_ADMIN_PORT}" \
      -u "${PROXYSQL_ADMIN_USER}" -p"${PROXYSQL_ADMIN_PASS}" \
      -e "SELECT rule_id, match_pattern, destination_hostgroup FROM runtime_mysql_query_rules WHERE rule_id BETWEEN 990001 AND 990010 ORDER BY rule_id;"

echo "==> Done."
