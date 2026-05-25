#!/usr/bin/env bash
#
# Synthetic traffic generator for the bintrail demo.
#
# Connects to ProxySQL (which forwards to the appdb backend) and
# emits a continuous stream of INSERTs, UPDATEs, and DELETEs against
# `orders`. The flow:
#
#   - INSERT a new order with a randomized customer_id, product,
#     qty, price.
#   - With some probability, UPDATE the status of a random existing
#     order (paid → shipped, pending → paid, etc.).
#   - With smaller probability, DELETE an order at random.
#
# Order id=5 is the demo's protected "VIP" row — traffic never
# touches it so the demo script's flashback / recover steps remain
# deterministic. Range: id ≥ 100 (the seeded set is 1..50) for new
# inserts, so we don't collide with the original 50.
#
# Sleep jittered 0.3-1.5s between operations so the event stream
# looks human-paced but doesn't drown the index in volume.

set -euo pipefail

: "${MYSQL_HOST:=proxysql}"
: "${MYSQL_PORT:=6033}"
: "${MYSQL_USER:=app}"
: "${MYSQL_PASS:=apppw}"
: "${MYSQL_DB:=appdb}"
: "${PROTECTED_ID:=5}"

MYSQL_CMD=(mysql -h "${MYSQL_HOST}" -P "${MYSQL_PORT}"
           -u "${MYSQL_USER}" -p"${MYSQL_PASS}" "${MYSQL_DB}"
           --batch --skip-column-names)

PRODUCTS=("Helmet" "Saddle" "Bike Lock" "Bottle" "Chain" "Pedals" "Tire" "Tube"
          "Bar Tape" "Gloves" "Jersey" "Pump" "Tool" "Light" "Cable" "Cleats"
          "Bag" "Pannier" "Lock" "Bell" "Mirror" "Mount" "GPS" "Trainer")
STATUSES=("pending" "paid" "shipped" "cancelled")

echo "==> Waiting for ProxySQL at ${MYSQL_HOST}:${MYSQL_PORT}"
until "${MYSQL_CMD[@]}" -e "SELECT 1" >/dev/null 2>&1; do
    sleep 1
done
echo "==> ProxySQL is reachable, starting traffic"

rand() { echo $((RANDOM % $1)); }
rand_product() { echo "${PRODUCTS[$(rand ${#PRODUCTS[@]})]}"; }
rand_status() { echo "${STATUSES[$(rand ${#STATUSES[@]})]}"; }

INSERT_COUNT=0
UPDATE_COUNT=0
DELETE_COUNT=0

while true; do
    action=$(rand 10)

    if [[ $action -lt 6 ]]; then
        customer=$((100 + $(rand 900)))
        product=$(rand_product)
        qty=$((1 + $(rand 4)))
        price=$((500 + $(rand 50000)))
        status=$(rand_status)
        "${MYSQL_CMD[@]}" -e "INSERT INTO orders (customer_id, product, qty, price_cents, status) VALUES (${customer}, '${product}', ${qty}, ${price}, '${status}');" \
            && INSERT_COUNT=$((INSERT_COUNT + 1))

    elif [[ $action -lt 9 ]]; then
        new_status=$(rand_status)
        "${MYSQL_CMD[@]}" -e "UPDATE orders SET status = '${new_status}' WHERE id <> ${PROTECTED_ID} ORDER BY RAND() LIMIT 1;" \
            && UPDATE_COUNT=$((UPDATE_COUNT + 1))

    else
        "${MYSQL_CMD[@]}" -e "DELETE FROM orders WHERE id <> ${PROTECTED_ID} AND id > 50 ORDER BY RAND() LIMIT 1;" \
            && DELETE_COUNT=$((DELETE_COUNT + 1))
    fi

    if (( (INSERT_COUNT + UPDATE_COUNT + DELETE_COUNT) % 20 == 0 )); then
        echo "[$(date -u +%H:%M:%S)] traffic: inserts=${INSERT_COUNT} updates=${UPDATE_COUNT} deletes=${DELETE_COUNT}"
    fi

    sleep_ms=$((300 + $(rand 1200)))
    sleep "0.$(printf '%03d' $sleep_ms)"
done
