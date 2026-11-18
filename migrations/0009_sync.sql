-- Step 10: a revision number that changes whenever anything on the shopping
-- list changes, so other devices can tell cheaply that they are out of date;
-- and the ids of offline changes already applied, so a replay never doubles a purchase.
CREATE TABLE list_rev (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    n  INTEGER NOT NULL
);
INSERT INTO list_rev (id, n) VALUES (1, 0);

CREATE TRIGGER list_items_ins AFTER INSERT ON list_items BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER list_items_upd AFTER UPDATE ON list_items BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER list_items_del AFTER DELETE ON list_items BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER purchases_ins AFTER INSERT ON purchases BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER purchases_upd AFTER UPDATE ON purchases BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER purchases_del AFTER DELETE ON purchases BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER stores_chg AFTER UPDATE ON stores BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER stores_ins AFTER INSERT ON stores BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER stores_del AFTER DELETE ON stores BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER aisles_chg AFTER UPDATE ON aisles BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER aisles_ins AFTER INSERT ON aisles BEGIN UPDATE list_rev SET n = n + 1; END;
CREATE TRIGGER aisles_del AFTER DELETE ON aisles BEGIN UPDATE list_rev SET n = n + 1; END;

CREATE TABLE applied_ops (
    op TEXT PRIMARY KEY,
    at INTEGER NOT NULL
);
