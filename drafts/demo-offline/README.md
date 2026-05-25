# Bintrail — demo offline con ProxySQL

Stack 100% local para demostrar time-travel SQL sobre MySQL + bintrail + ProxySQL, **sin internet** (más allá de pullear las imágenes de Docker la primera vez). Pensado para presentar en vivo.

## Qué levanta

```
host mysql client ──► 127.0.0.1:16033 ──► ProxySQL ──┬──► hostgroup 990 ──► MySQL appdb
                                                      │
                                                      └──► hostgroup 991 ──► bintrail shim
                                                                                  │
                                                                                  └──► MySQL bintrail_index
```

7 contenedores: `mysql`, `proxysql`, `bintrail-init` (one-shot), `bintrail-stream`, `bintrail-shim` (sidecar de proxysql), `proxysql-setup` (one-shot), `traffic` (tráfico sintético) y `cli` (shell con bintrail + mysql client siempre activo).

## Pre-flight

Solo necesitás:

- **Docker + docker compose** (v2)
- **Make** (opcional, los comandos están todos a la vista en el `Makefile`)
- **Puertos libres en localhost**: `13306` (MySQL), `16032`/`16033` (ProxySQL admin/data)

**Imágenes a pullear (esto SÍ necesita internet, una sola vez):**

```sh
docker pull mysql:8.0
docker pull proxysql/proxysql:2.6.6
docker pull golang:1.25-bookworm     # build stage
docker pull debian:bookworm-slim     # runtime stage
```

A partir de acá todo corre offline.

## Levantar

```sh
cd drafts/demo-offline
make up
```

La primera vez compila el binario de bintrail dentro del contenedor (~1-2 min). Después arranca todo el stack. Cuando termine:

```sh
make stream-logs
# Tenés que ver algo como:
#   INFO connected to source mysql=mysql:3306 mode=gtid
#   INFO indexed events file=mysql-bin.000001 count=N
# Ctrl-C cuando lo veas.
```

Verificá que el índice está recibiendo eventos:

```sh
make wait    # bloquea hasta que haya al menos 1 evento
make status  # muestra particiones, conteo, ventana temporal
make events  # muestra los últimos 20 eventos indexados
```

> En este punto el tráfico sintético ya está corriendo en `appdb.orders` (id > 50). El stream los está indexando. La fila `id=5` no recibe tráfico (está protegida) — la demo la mueve a mano.

## Demo — Parte 1: time-travel sobre una sola fila

**Importante**: el shim parsea `AS OF '<ts>'` como **literal de string**, NO como variable MySQL (`@t0`). Por eso la demo se maneja desde un shell bash con helpers, no desde el prompt interactivo de `mysql>`. El archivo `demo.sh` está montado en `/etc/bintrail/demo.sh` y `make cli` lo carga automáticamente.

Abrí el shell:

```sh
make cli
```

Vas a ver `demo helpers loaded — try: now, show_pk 5, change_qty 5 2, flashback 5 "$T1"`. Estás adentro del contenedor `cli`, con bintrail + mysql client + los helpers de `demo.sh` ya cargados.

### Paso 1 — Estado inicial de la fila VIP

```sh
show_pk 5
```

Debe mostrar:

```
id:          5
customer_id: 999
product:     Carbon Frame
qty:         1
price_cents: 189900
status:      paid
```

### Paso 2 — Capturar T0 (antes de cualquier cambio)

```sh
T0=$(now); echo "T0 = $T0"
```

> **Nota**: la primera UPDATE/DELETE capturada por bintrail incluye el `row_before` con el estado original. Antes de que haya pasado *cualquier* evento sobre `id=5`, el shim no tiene de dónde reconstruir; por eso usamos `_diff` (que lee el `row_before` del primer evento) para mostrar el estado pre-cambio. Time-travel "puro" (`flashback`) funciona para todos los instantes ≥ al primer evento.

### Paso 3 — Hacer cambios deliberados sobre `id=5`

```sh
# Cambio 1: la cantidad se modifica
change_qty 5 2
T1=$(now); echo "T1 = $T1"

# Cambio 2: el estado pasa a 'shipped'
change_status 5 shipped
T2=$(now); echo "T2 = $T2"

# "Oops" — borramos la fila por error
delete_order 5
T3=$(now); echo "T3 = $T3"
```

(Los helpers ya hacen `sleep 2` entre operaciones para que cada evento caiga en un segundo distinto — recordá que `event_timestamp` es `DATETIME` sin fracciones.)

Verificá que la fila está realmente borrada:

```sh
show_pk 5
# Empty set
```

### Paso 4 — Time-travel: ver el estado en cada instante

```sh
# Estado tras la primera UPDATE (qty=2, status sigue 'paid')
flashback 5 "$T1"

# Estado tras la segunda UPDATE (qty=2, status='shipped')
flashback 5 "$T2"

# Estado tras el DELETE (la fila no existe en ese instante)
flashback 5 "$T3"
# Devuelve un resultset vacío con una sola columna sentinela `_flashback`
```

> `_snapshot` es hoy sinónimo de `_flashback` — `snapshot_at 5 "$T2"` devuelve lo mismo.

### Paso 5 — Diff: la historia completa de la fila

```sh
diff_pk 5 "$T0" "$(now)"
```

Devuelve los 3 eventos (UPDATE, UPDATE, DELETE). El `row_before` del primer evento tiene el estado original que perdiste (`qty=1, status='paid'`); el `row_after` del último es `NULL` (era un DELETE).

`event_type`: `1=INSERT, 2=UPDATE, 3=DELETE`.

### Paso 6 — Recuperar la fila

```sh
recover_pk 5 /tmp/recover-5.sql
```

El helper invoca `bintrail recover --pk=5 --output=/tmp/recover-5.sql` y te muestra el SQL generado. Algo como:

```sql
-- recovery for appdb.orders pk=5 (3 events reversed)
INSERT INTO appdb.orders (id, customer_id, product, qty, price_cents, status, ...) VALUES (5, 999, 'Carbon Frame', 2, 189900, 'shipped', ...);
UPDATE appdb.orders SET status='paid' WHERE id=5;
UPDATE appdb.orders SET qty=1 WHERE id=5;
```

bintrail revierte los eventos en orden inverso (más reciente primero), así que el resultado final restaura el estado original.

Aplicalo:

```sh
apply_sql /tmp/recover-5.sql
show_pk 5
```

Debe mostrar `qty=1, status='paid'` — el estado original.

---

## Demo — Parte 2 (bonus): recovery masivo por ventana de tiempo

Simulamos un incidente: un script suelto que cancela todas las órdenes pagadas en los últimos N segundos. Para que la ventana de recovery quede limpia, **pausá el tráfico sintético antes** — si no, el SQL generado va a incluir reversales de los INSERT/UPDATE/DELETE de `traffic` y el público pierde el hilo narrativo.

```sh
# Pausa el tráfico sintético (desde fuera del cli, en tu terminal del host)
docker compose stop traffic
```

Volvé al shell del `cli`:

```sh
# Marcá el momento previo al "incidente"
BEFORE=$(now); echo "BEFORE = $BEFORE"

# "Incidente": cancela todo lo que estaba 'paid'
mysql_app -e "UPDATE orders SET status='cancelled' WHERE status='paid';"
mysql_app -e "SELECT ROW_COUNT() AS rows_cancelled;"

sleep 2
AFTER=$(now); echo "AFTER = $AFTER"
```

Mostrá el daño:

```sh
mysql_app -e "SELECT status, COUNT(*) FROM orders GROUP BY status;"
```

Time-travel: ver el estado de las primeras órdenes ANTES del incidente (LIMIT no se puede en SQL — la regex del shim no lo admite — así que lo cortamos client-side):

```sh
mysql_app -e "SELECT id, status FROM _flashback.orders AS OF '$BEFORE';" | head -20
```

Recovery automático de toda la ventana:

```sh
recover_window "$BEFORE" "$AFTER" /tmp/recover-mass.sql
head -10 /tmp/recover-mass.sql            # vistazo
apply_sql /tmp/recover-mass.sql
mysql_app -e "SELECT status, COUNT(*) FROM orders GROUP BY status;"   # daño deshecho
```

Y reanudá el tráfico cuando termines:

```sh
docker compose start traffic   # desde tu terminal del host
```

> **Cuidado**: `recover_window` reversiona TODOS los eventos en la ventana. En prod siempre acotá con `--pks` o filtros más finos.

---

## Demo — Parte 3 (bonus): SELECT completo time-travel

El shim acepta queries `_flashback`/`_snapshot` SIN `WHERE` (reconstrucción de toda la tabla a ese instante) pero **no** acepta `ORDER BY` ni `LIMIT` después del `AS OF` (la regex termina en `\s*;?\s*$`). Para muestrear o ordenar, hacelo del lado del cliente:

```sh
# "Cuántas filas había hace 1 minuto"
flashback_full_count "$(date -u -d '1 minute ago' '+%Y-%m-%d %H:%M:%S')"

# Reconstrucción completa — solo un par de columnas para que el output sea manejable.
# El sort y el head se hacen client-side, no en SQL.
ONE_MIN_AGO=$(date -u -d '1 minute ago' '+%Y-%m-%d %H:%M:%S')
mysql_app -e "SELECT id, product, status FROM _flashback.orders AS OF '$ONE_MIN_AGO';" | sort -n | head -20
```

Cap interno: 100k filas; si lo excede devuelve `ER_TOO_BIG_SELECT (1104)` con sugerencia de acotar el AS OF o agregar un WHERE por PK.

> Shapes soportadas en la regex actual:
> - `SELECT * FROM _flashback.<t> AS OF '<ts>' [WHERE <pk> = <val>]`
> - `SELECT <col>[, <col>...] FROM _flashback.<t> AS OF '<ts>' [WHERE <pk> = <val>]`
> - `SELECT * FROM _diff.<t> BETWEEN '<t1>' AND '<t2>' WHERE <pk> = <val>`  (WHERE obligatorio)
>
> Funciones (`COUNT(*)`, `MAX(...)`), aliases, `ORDER BY`, `LIMIT`, `JOIN`: NO. Si los necesitás, transformá client-side o pasá la salida a otro tool (`duckdb`, `pandas`, etc.).

---

## Cómo está armado por dentro

| Componente | Imagen | Función |
|---|---|---|
| `mysql` | `mysql:8.0` | Source DB (`appdb`) + index DB (`bintrail_index`) en la misma instancia. ROW + FULL + GTID. |
| `proxysql` | `proxysql/proxysql:2.6.6` | Frente único; routea queries a MySQL real o al shim según el schema. |
| `bintrail-init` | `bintrail-demo` | One-shot: `bintrail init` + `bintrail snapshot`. Exit 0 al terminar. |
| `bintrail-stream` | `bintrail-demo` | Replica binlogs de `appdb` al índice en tiempo real. |
| `bintrail-shim` | `bintrail-demo` | Sidecar de ProxySQL. Sirve `_flashback`/`_diff`/`_snapshot` en `127.0.0.1:3308`. |
| `proxysql-setup` | `bintrail-demo` | One-shot: `bintrail proxysql-config` → admin de ProxySQL. |
| `traffic` | `bintrail-demo` | INSERT/UPDATE/DELETE sintéticos contra `appdb` (no toca `id=5`). |
| `cli` | `bintrail-demo` | Shell siempre activo con `mysql` client + binario `bintrail`. |

**Por qué el shim es sidecar de proxysql**: `network_mode: service:proxysql` hace que el shim corra en el mismo namespace de red. ProxySQL lo alcanza por `127.0.0.1:3308` — que es lo que `bintrail proxysql-config` hardcodea. Cualquier otra topología requiere editar el SQL generado.

**Por qué hay un usuario `app` igual en MySQL y en `shim.yaml`**: ProxySQL forwardea las credenciales tal cual al backend (no re-encripta), así que el mismo usuario tiene que existir y autenticarse igual en ambos lados.

## Cleanup

```sh
make down      # frena los contenedores, mantiene volúmenes y red
make clean     # frena + borra volúmenes + borra la imagen bintrail-demo
```

## Troubleshooting

### `make up` se cuelga en "Building bintrail-demo"

La primera build compila el binario (CGO + DuckDB). En un laptop razonable: 60-120s. Si es la primera vez que descargás `golang:1.25-bookworm`, sumále otros 60s. `docker compose build --progress=plain` muestra qué está pasando.

### `bintrail-stream` reinicia constantemente

Mirá los logs:

```sh
make stream-logs
```

Causas típicas:
- MySQL todavía no terminó la inicialización (la seed.sql tarda) → esperá 30s y mirá de nuevo
- `repl` no tiene permisos REPLICATION SLAVE → confirmar con `docker compose exec cli mysql -h mysql -uroot -pdemoroot -e "SHOW GRANTS FOR 'repl'@'%'"`
- `binlog_row_image` no es FULL → el comando del MySQL container lo fija, pero si lo editaste, `bintrail stream` aborta con un error explícito

### Las queries `_flashback` devuelven vacío aunque haya datos

Tres causas posibles, documentadas en `docs/time-travel-sql.md`:

1. La fila nunca fue tocada por un evento del stream (sólo está en el seed inicial). Solución: tocá la fila (UPDATE) antes de probar el flashback.
2. El último evento al-o-antes del timestamp es un DELETE — Oracle `AS OF` semántico: si no existía en ese instante, vacío.
3. El timestamp cae fuera de la ventana del índice. `make status` muestra la ventana.

Para distinguir (1) y (2): pegále `_diff` a la misma PK y rango; si devuelve filas, era (2).

### ProxySQL devuelve "User 'app' has no privileges"

`proxysql-setup` no llegó a aplicar el SQL de routing. Mirá:

```sh
docker compose logs proxysql-setup
```

Si quedó esperando al admin, re-corré: `docker compose up -d proxysql-setup`.

### Quiero ver el SQL que generó `bintrail proxysql-config`

```sh
docker compose exec cli bintrail proxysql-config \
    --shim-config=/etc/bintrail/shim.yaml \
    --out=- 2>/dev/null
```

(El env var `BINTRAIL_SOURCE_DSN` ya está seteado en el contenedor `cli`.)

### Quiero conectar con mi propio mysql client desde el host

```sh
mysql -h 127.0.0.1 -P 16033 -u app -papppw appdb
```

(Funciona igual que `make mysql-cli`, sólo que usás tu cliente local.)

---

## Archivos

```
drafts/demo-offline/
├── README.md                    # este archivo
├── Makefile                     # convenience targets (up/down/cli/logs/...)
├── docker-compose.yml           # stack
├── Dockerfile                   # imagen bintrail-demo (binario + mysql client + bash)
├── seed.sql                     # schema appdb + 50 orders + users
├── shim.yaml                    # tenant config del shim
├── proxysql.cnf                 # config base de ProxySQL
├── apply-proxysql-config.sh     # one-shot: aplica el SQL de routing
├── traffic.sh                   # generador de tráfico sintético
└── demo.sh                      # helpers de bash para la demo (flashback, recover_pk, ...)
```
