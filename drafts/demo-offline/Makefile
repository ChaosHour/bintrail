# Bintrail offline demo — convenience targets.
#
# Wraps `docker compose` so the presenter doesn't have to remember
# the long invocations. All targets are no-op safe to re-run.

COMPOSE := docker compose

.PHONY: help build up wait down clean logs stream-logs shim-logs traffic-logs \
        cli mysql-cli psql status events sample

help:
	@echo "Bintrail offline demo — Makefile targets"
	@echo ""
	@echo "  make build         Build the bintrail-demo image"
	@echo "  make up            Bring up the full stack (mysql + proxysql + bintrail + traffic)"
	@echo "  make wait          Block until the index has captured at least one event"
	@echo "  make down          Stop the stack (keeps volumes/networks)"
	@echo "  make clean         Stop and delete everything (volumes, networks, images)"
	@echo ""
	@echo "  make logs          Tail logs from all services"
	@echo "  make stream-logs   Tail bintrail-stream logs"
	@echo "  make shim-logs     Tail bintrail-shim logs"
	@echo "  make traffic-logs  Tail traffic generator logs"
	@echo ""
	@echo "  make cli           Open a bash shell with bintrail + mysql client"
	@echo "  make mysql-cli     Open a mysql prompt connected to ProxySQL as 'app'"
	@echo "  make psql          Alias for mysql-cli"
	@echo ""
	@echo "  make status        Run 'bintrail status' against the index"
	@echo "  make events        Run 'bintrail query --limit 20 --order DESC'"
	@echo "  make sample        Show the current state of orders id=1..5"
	@echo ""
	@echo "Demo script: see README.md"

build:
	$(COMPOSE) build

up: build
	$(COMPOSE) up -d
	@echo ""
	@echo "Stack starting. Tail bintrail-stream until you see 'streaming':"
	@echo "  make stream-logs"
	@echo ""
	@echo "Connect as the demo's app user (through ProxySQL):"
	@echo "  make mysql-cli"
	@echo ""
	@echo "Then follow README.md for the demo script."

wait:
	@echo "Waiting for bintrail-stream to capture at least one event…"
	@for i in $$(seq 1 60); do \
	    n=$$($(COMPOSE) exec -T cli mysql -h mysql -uroot -pdemoroot bintrail_index \
	         -BNe 'SELECT COUNT(*) FROM binlog_events' 2>/dev/null || echo 0); \
	    if [ "$$n" -gt 0 ]; then \
	        echo "  ready ($$n events indexed)"; \
	        exit 0; \
	    fi; \
	    sleep 1; \
	done; \
	echo "  timeout waiting for events"; exit 1

down:
	$(COMPOSE) down

clean:
	$(COMPOSE) down -v --rmi local --remove-orphans

logs:
	$(COMPOSE) logs -f --tail=50

stream-logs:
	$(COMPOSE) logs -f bintrail-stream

shim-logs:
	$(COMPOSE) logs -f bintrail-shim

traffic-logs:
	$(COMPOSE) logs -f traffic

cli:
	$(COMPOSE) exec cli bash --rcfile /etc/bintrail/demo.sh

mysql-cli:
	$(COMPOSE) exec cli mysql -h proxysql -P 6033 -u app -papppw appdb

psql: mysql-cli

status:
	$(COMPOSE) exec cli bintrail status --index-dsn='root:demoroot@tcp(mysql:3306)/bintrail_index'

events:
	$(COMPOSE) exec cli bintrail query \
	    --index-dsn='root:demoroot@tcp(mysql:3306)/bintrail_index' \
	    --schema=appdb --table=orders --limit=20 --order=DESC

sample:
	$(COMPOSE) exec cli mysql -h mysql -uroot -pdemoroot appdb \
	    -e "SELECT id, customer_id, product, qty, price_cents, status FROM orders WHERE id <= 5;"
