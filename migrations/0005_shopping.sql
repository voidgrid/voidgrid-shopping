-- Stores and their aisles (the number is the walking order).
CREATE TABLE stores (
  id       INTEGER PRIMARY KEY,
  name     TEXT NOT NULL UNIQUE COLLATE NOCASE,
  position INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE aisles (
  id       INTEGER PRIMARY KEY,
  store_id INTEGER NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
  name     TEXT NOT NULL COLLATE NOCASE,
  position INTEGER NOT NULL DEFAULT 0,
  UNIQUE (store_id, name)
);

-- Learned as items are assigned: where an item usually is bought, and its aisle in each store.
CREATE TABLE item_places (
  key      TEXT PRIMARY KEY,
  store_id INTEGER NOT NULL REFERENCES stores(id) ON DELETE CASCADE
);
CREATE TABLE item_aisles (
  store_id INTEGER NOT NULL REFERENCES stores(id) ON DELETE CASCADE,
  key      TEXT NOT NULL,
  aisle_id INTEGER NOT NULL REFERENCES aisles(id) ON DELETE CASCADE,
  PRIMARY KEY (store_id, key)
);

-- Things bought regularly (food and not): copied to the list on request.
CREATE TABLE staples (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  key        TEXT NOT NULL UNIQUE,
  quantity   REAL NOT NULL DEFAULT 0,
  unit       TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  store_id   INTEGER REFERENCES stores(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL
);

CREATE TABLE list_items (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  key        TEXT NOT NULL,
  quantity   REAL NOT NULL DEFAULT 0,
  unit       TEXT NOT NULL DEFAULT '',
  pack       TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  store_id   INTEGER REFERENCES stores(id) ON DELETE SET NULL,
  aisle_id   INTEGER REFERENCES aisles(id) ON DELETE SET NULL,
  state      TEXT NOT NULL DEFAULT 'need' CHECK (state IN ('need', 'have', 'bought')),
  from_staple INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  changed_at INTEGER NOT NULL
);
CREATE INDEX list_items_key ON list_items(key);

-- Planned meals already added to the list, so adding again doesn't double them.
CREATE TABLE list_added_entries (
  plan_entry_id INTEGER PRIMARY KEY REFERENCES plan_entries(id) ON DELETE CASCADE
);

-- Each purchase of an item: quantity, price per package or per weight, brand.
CREATE TABLE purchases (
  id          INTEGER PRIMARY KEY,
  item_id     INTEGER NOT NULL REFERENCES list_items(id) ON DELETE CASCADE,
  quantity    REAL NOT NULL,
  price_cents INTEGER NOT NULL CHECK (price_cents >= 0),
  weight_unit TEXT NOT NULL DEFAULT '' CHECK (weight_unit IN ('', 'lb', 'oz', 'kg', 'g')),
  weight      REAL NOT NULL DEFAULT 0,
  brand       TEXT NOT NULL DEFAULT '',
  total_cents INTEGER NOT NULL,
  created_at  INTEGER NOT NULL
);
CREATE INDEX purchases_item ON purchases(item_id);

-- The last price paid per item, store and brand (store 0: no store).
CREATE TABLE item_prices (
  key         TEXT NOT NULL,
  store_id    INTEGER NOT NULL DEFAULT 0,
  brand       TEXT NOT NULL DEFAULT '' COLLATE NOCASE,
  price_cents INTEGER NOT NULL,
  weight_unit TEXT NOT NULL DEFAULT '',
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (key, store_id, brand)
);
