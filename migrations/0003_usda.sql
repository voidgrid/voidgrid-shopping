-- USDA FoodData Central (public domain): food names and their household
-- measures. ml is the volume of `amount` of `label` when the measure is a
-- volume (cup, tbsp ...), NULL otherwise; grams / ml is the density.
CREATE TABLE usda_foods (
  fdc_id      INTEGER PRIMARY KEY,
  description TEXT NOT NULL,
  data_type   TEXT NOT NULL
);

CREATE TABLE usda_portions (
  id     INTEGER PRIMARY KEY,
  fdc_id INTEGER NOT NULL REFERENCES usda_foods(fdc_id) ON DELETE CASCADE,
  amount REAL NOT NULL,
  label  TEXT NOT NULL,
  grams  REAL NOT NULL,
  ml     REAL
);
CREATE INDEX usda_portions_fdc ON usda_portions(fdc_id);

CREATE VIRTUAL TABLE usda_foods_fts USING fts5(description, tokenize = 'porter unicode61');
