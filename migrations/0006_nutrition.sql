-- USDA nutrients per 100 g (energy in kcal; protein, carbohydrate and fat in g).
CREATE TABLE usda_nutrients (
  fdc_id  INTEGER PRIMARY KEY REFERENCES usda_foods(fdc_id) ON DELETE CASCADE,
  kcal    REAL NOT NULL,
  protein REAL NOT NULL,
  carbs   REAL NOT NULL,
  fat     REAL NOT NULL
);

-- Foods a person typed in by hand, reusable across recipes. Values are per 100 g.
CREATE TABLE food_records (
  id         INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  kcal       REAL NOT NULL,
  protein    REAL NOT NULL,
  carbs      REAL NOT NULL,
  fat        REAL NOT NULL,
  unit_g     REAL NOT NULL DEFAULT 0,
  g_per_ml   REAL NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

-- Open Food Facts products looked up by barcode (ODbL data), cached so each barcode is fetched once.
CREATE TABLE off_products (
  barcode       TEXT PRIMARY KEY,
  name          TEXT NOT NULL DEFAULT '',
  brand         TEXT NOT NULL DEFAULT '',
  kcal          REAL NOT NULL DEFAULT 0,
  protein       REAL NOT NULL DEFAULT 0,
  carbs         REAL NOT NULL DEFAULT 0,
  fat           REAL NOT NULL DEFAULT 0,
  has_nutrition INTEGER NOT NULL DEFAULT 0,
  serving_g     REAL NOT NULL DEFAULT 0,
  package_g     REAL NOT NULL DEFAULT 0,
  fetched_at    INTEGER NOT NULL
);

-- Which food an ingredient (by its list key) takes its nutrition from. One choice per ingredient.
CREATE TABLE ingredient_foods (
  key        TEXT PRIMARY KEY,
  source     TEXT NOT NULL CHECK (source IN ('usda', 'off', 'manual')),
  fdc_id     INTEGER REFERENCES usda_foods(fdc_id) ON DELETE CASCADE,
  barcode    TEXT REFERENCES off_products(barcode) ON DELETE CASCADE,
  record_id  INTEGER REFERENCES food_records(id) ON DELETE CASCADE,
  unit_g     REAL NOT NULL DEFAULT 0,
  updated_at INTEGER NOT NULL,
  CHECK ((source = 'usda') = (fdc_id IS NOT NULL) AND (source = 'off') = (barcode IS NOT NULL) AND (source = 'manual') = (record_id IS NOT NULL))
);

-- Nutrition entered by hand for a whole recipe, per serving; it replaces the calculated figures.
CREATE TABLE recipe_nutrition (
  recipe_id INTEGER PRIMARY KEY REFERENCES recipes(id) ON DELETE CASCADE,
  kcal      REAL NOT NULL,
  protein   REAL NOT NULL,
  carbs     REAL NOT NULL,
  fat       REAL NOT NULL
);
