-- The meal plan. A slot of a day holds recipes, plain notes and "leftovers" markers.
CREATE TABLE plan_entries (
  id         INTEGER PRIMARY KEY,
  date       TEXT NOT NULL CHECK (date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
  slot       TEXT NOT NULL CHECK (slot IN ('breakfast', 'lunch', 'snack', 'dinner')),
  kind       TEXT NOT NULL CHECK (kind IN ('recipe', 'leftovers', 'note')),
  recipe_id  INTEGER REFERENCES recipes(id) ON DELETE CASCADE,
  servings   INTEGER NOT NULL DEFAULT 0,
  note       TEXT NOT NULL DEFAULT '',
  position   INTEGER NOT NULL DEFAULT 0,
  created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  CHECK ((kind = 'recipe') = (recipe_id IS NOT NULL))
);
CREATE INDEX plan_entries_date ON plan_entries(date);
CREATE INDEX plan_entries_recipe ON plan_entries(recipe_id);

-- A note on a day (the Notes row), optionally tied to a later meal: "thaw the chicken" for Thursday dinner.
CREATE TABLE day_notes (
  id         INTEGER PRIMARY KEY,
  date       TEXT NOT NULL CHECK (date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
  text       TEXT NOT NULL,
  for_date   TEXT CHECK (for_date IS NULL OR for_date GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]'),
  for_slot   TEXT CHECK (for_slot IS NULL OR for_slot IN ('breakfast', 'lunch', 'snack', 'dinner')),
  created_by INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  CHECK ((for_date IS NULL) = (for_slot IS NULL))
);
CREATE INDEX day_notes_date ON day_notes(date);
