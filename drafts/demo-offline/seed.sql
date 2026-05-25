-- Bintrail offline demo: schema + users + initial data.
--
-- Two databases in one MySQL container:
--
--   appdb           — the simulated production database. Holds the
--                     `orders` table with 50 seeded rows. This is
--                     what the application (and the traffic generator)
--                     write to; it's also where `_flashback.orders`
--                     resolves from.
--
--   bintrail_index  — created empty by MySQL here; `bintrail init`
--                     populates it with `binlog_events`,
--                     `schema_snapshots`, `index_state`,
--                     `stream_state`, `archive_state` etc. on first
--                     run.
--
-- Three MySQL users:
--
--   root / demoroot — admin, used by bintrail-init and the demo
--                     for ad-hoc inspection.
--
--   app / apppw     — application user. Has SELECT/INSERT/UPDATE/
--                     DELETE on appdb.*. This is the user the
--                     traffic generator and the demo presenter
--                     use when connecting through ProxySQL —
--                     because ProxySQL forwards credentials as-is
--                     to the backend, the same user must exist on
--                     both sides with the same password.
--
--   repl / replpw   — replication user for `bintrail stream`. Has
--                     REPLICATION SLAVE and REPLICATION CLIENT
--                     globally plus SELECT on appdb.* (for the
--                     initial snapshot's INFORMATION_SCHEMA reads).

CREATE DATABASE IF NOT EXISTS appdb CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
CREATE DATABASE IF NOT EXISTS bintrail_index CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

USE appdb;

CREATE TABLE orders (
    id           INT          PRIMARY KEY AUTO_INCREMENT,
    customer_id  INT          NOT NULL,
    product      VARCHAR(64)  NOT NULL,
    qty          INT          NOT NULL,
    price_cents  INT          NOT NULL,
    status       ENUM('pending','paid','shipped','cancelled') NOT NULL DEFAULT 'pending',
    created_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME     NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
) ENGINE=InnoDB;

-- 50 seeded orders. id=5 is the "VIP order" the demo script
-- targets — its initial values are pinned (sku, qty, price) so the
-- presenter can visually verify the flashback matches.
INSERT INTO orders (id, customer_id, product, qty, price_cents, status) VALUES
    ( 1, 101, 'Helmet',         1,  4500, 'paid'),
    ( 2, 102, 'Saddle',         2,  6000, 'shipped'),
    ( 3, 103, 'Bike Lock',      1,  2500, 'paid'),
    ( 4, 104, 'Bottle Cage',    3,   900, 'pending'),
    ( 5, 999, 'Carbon Frame',   1, 189900, 'paid'),
    ( 6, 106, 'Pedals',         1,  3500, 'shipped'),
    ( 7, 107, 'Chain',          1,  1800, 'paid'),
    ( 8, 108, 'Brake Pads',     2,  1200, 'pending'),
    ( 9, 109, 'Tire',           2,  4500, 'paid'),
    (10, 110, 'Inner Tube',     4,   400, 'shipped'),
    (11, 111, 'Bar Tape',       1,  1500, 'paid'),
    (12, 112, 'Gloves',         1,  2200, 'pending'),
    (13, 113, 'Helmet',         1,  4500, 'paid'),
    (14, 114, 'Jersey',         1,  6500, 'paid'),
    (15, 115, 'Shorts',         1,  7500, 'shipped'),
    (16, 116, 'Pump',           1,  3000, 'paid'),
    (17, 117, 'Multi-tool',     1,  2000, 'pending'),
    (18, 118, 'Bike Rack',      1,  9500, 'paid'),
    (19, 119, 'Light Set',      1,  5500, 'shipped'),
    (20, 120, 'Computer',       1, 12500, 'paid'),
    (21, 121, 'Wheelset',       1, 54000, 'paid'),
    (22, 122, 'Fork',           1, 28000, 'shipped'),
    (23, 123, 'Bottom Bracket', 1,  4500, 'paid'),
    (24, 124, 'Headset',        1,  3800, 'pending'),
    (25, 125, 'Stem',           1,  2500, 'paid'),
    (26, 126, 'Handlebar',      1,  4200, 'shipped'),
    (27, 127, 'Seatpost',       1,  3500, 'paid'),
    (28, 128, 'Derailleur',     1,  8500, 'paid'),
    (29, 129, 'Cassette',       1,  5500, 'pending'),
    (30, 130, 'Crankset',       1, 12500, 'paid'),
    (31, 131, 'Shoes',          1,  9500, 'shipped'),
    (32, 132, 'Cleats',         1,  1500, 'paid'),
    (33, 133, 'Glasses',        1,  6000, 'pending'),
    (34, 134, 'Helmet',         1,  4500, 'paid'),
    (35, 135, 'Bottle',         2,   800, 'shipped'),
    (36, 136, 'Repair Kit',     1,  2500, 'paid'),
    (37, 137, 'Tire Lever',     3,   300, 'paid'),
    (38, 138, 'Patch Kit',      2,   500, 'pending'),
    (39, 139, 'Bike Cover',     1,  3500, 'paid'),
    (40, 140, 'Saddle Bag',     1,  2200, 'shipped'),
    (41, 141, 'Frame Bag',      1,  4500, 'paid'),
    (42, 142, 'Panniers',       1,  8500, 'paid'),
    (43, 143, 'Mud Guard',      2,  1800, 'pending'),
    (44, 144, 'Mirror',         1,  1200, 'paid'),
    (45, 145, 'Bell',           1,   500, 'shipped'),
    (46, 146, 'Phone Mount',    1,  2500, 'paid'),
    (47, 147, 'GPS',            1, 29500, 'paid'),
    (48, 148, 'Lock',           1,  4500, 'pending'),
    (49, 149, 'Trainer',        1, 35000, 'paid'),
    (50, 150, 'Floor Pump',     1,  4500, 'shipped');

CREATE USER 'app'@'%'  IDENTIFIED WITH mysql_native_password BY 'apppw';
GRANT SELECT, INSERT, UPDATE, DELETE ON appdb.* TO 'app'@'%';

CREATE USER 'repl'@'%' IDENTIFIED WITH mysql_native_password BY 'replpw';
GRANT REPLICATION SLAVE, REPLICATION CLIENT ON *.* TO 'repl'@'%';
GRANT SELECT ON appdb.* TO 'repl'@'%';

FLUSH PRIVILEGES;
