CREATE TABLE recipes (
  id              INTEGER PRIMARY KEY,
  title           TEXT NOT NULL,
  course          TEXT NOT NULL DEFAULT '',
  cuisine         TEXT NOT NULL DEFAULT '',
  main_ingredient TEXT NOT NULL DEFAULT '',
  description     TEXT NOT NULL DEFAULT '',
  source          TEXT NOT NULL DEFAULT '',
  url             TEXT NOT NULL DEFAULT '',
  prep_min        INTEGER NOT NULL DEFAULT 0,
  cook_min        INTEGER NOT NULL DEFAULT 0,
  total_min       INTEGER NOT NULL DEFAULT 0,
  servings        INTEGER NOT NULL DEFAULT 0,
  yield_text      TEXT NOT NULL DEFAULT '',
  ingredients     TEXT NOT NULL DEFAULT '',
  directions      TEXT NOT NULL DEFAULT '',
  rating          INTEGER NOT NULL DEFAULT 0 CHECK (rating BETWEEN 0 AND 5),
  photo           TEXT NOT NULL DEFAULT '',
  photo_src       TEXT NOT NULL DEFAULT '',
  created_by      INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at      INTEGER NOT NULL,
  updated_at      INTEGER NOT NULL
);
CREATE INDEX recipes_course ON recipes(course);
CREATE INDEX recipes_cuisine ON recipes(cuisine);
CREATE INDEX recipes_pending_photo ON recipes(photo_src) WHERE photo_src <> '' AND photo = '';

CREATE TABLE recipe_tags (
  recipe_id INTEGER NOT NULL REFERENCES recipes(id) ON DELETE CASCADE,
  tag       TEXT NOT NULL COLLATE NOCASE,
  PRIMARY KEY (recipe_id, tag)
);
CREATE INDEX recipe_tags_tag ON recipe_tags(tag);

CREATE VIRTUAL TABLE recipes_fts USING fts5(title, body, tags, tokenize = 'porter unicode61');
