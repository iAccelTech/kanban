-- Initial schema. Times are Unix milliseconds. Rows with user content are archived
-- (archived_at) rather than deleted, as in Trello.

CREATE TABLE users (
  id          INTEGER PRIMARY KEY,
  email       TEXT    NOT NULL UNIQUE COLLATE NOCASE,
  name        TEXT    NOT NULL,
  pw_hash     TEXT    NOT NULL DEFAULT '',
  verified_at INTEGER,
  created     INTEGER NOT NULL
) STRICT;

CREATE TABLE workspaces (
  id      INTEGER PRIMARY KEY,
  name    TEXT    NOT NULL,
  created INTEGER NOT NULL
) STRICT;

CREATE TABLE boards (
  id           INTEGER PRIMARY KEY,
  workspace_id INTEGER NOT NULL REFERENCES workspaces(id),
  title        TEXT    NOT NULL,
  color        TEXT    NOT NULL,
  created_by   INTEGER NOT NULL REFERENCES users(id),
  created      INTEGER NOT NULL,
  archived_at  INTEGER,
  version      INTEGER NOT NULL DEFAULT 1
) STRICT;
CREATE INDEX boards_workspace ON boards(workspace_id);

CREATE TABLE board_members (
  board_id INTEGER NOT NULL REFERENCES boards(id),
  user_id  INTEGER NOT NULL REFERENCES users(id),
  role     TEXT    NOT NULL CHECK (role IN ('owner', 'editor', 'viewer')),
  PRIMARY KEY (board_id, user_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX board_members_user ON board_members(user_id);

CREATE TABLE board_stars (
  user_id  INTEGER NOT NULL REFERENCES users(id),
  board_id INTEGER NOT NULL REFERENCES boards(id),
  PRIMARY KEY (user_id, board_id)
) STRICT, WITHOUT ROWID;

CREATE TABLE lists (
  id          INTEGER PRIMARY KEY,
  board_id    INTEGER NOT NULL REFERENCES boards(id),
  title       TEXT    NOT NULL,
  rank        TEXT    NOT NULL,
  created     INTEGER NOT NULL,
  archived_at INTEGER,
  version     INTEGER NOT NULL DEFAULT 1
) STRICT;
CREATE INDEX lists_board_rank ON lists(board_id, rank);

CREATE TABLE cards (
  id               INTEGER PRIMARY KEY,
  board_id         INTEGER NOT NULL REFERENCES boards(id),
  list_id          INTEGER NOT NULL REFERENCES lists(id),
  title            TEXT    NOT NULL,
  description      TEXT    NOT NULL DEFAULT '',  -- Markdown source
  description_html TEXT    NOT NULL DEFAULT '',  -- sanitized render of description
  rank             TEXT    NOT NULL,
  due              INTEGER,
  due_done         INTEGER NOT NULL DEFAULT 0,
  created_by       INTEGER NOT NULL REFERENCES users(id),
  created          INTEGER NOT NULL,
  updated          INTEGER NOT NULL,
  archived_at      INTEGER,
  version          INTEGER NOT NULL DEFAULT 1
) STRICT;
CREATE INDEX cards_list_rank ON cards(list_id, rank);
CREATE INDEX cards_board ON cards(board_id);

CREATE TABLE labels (
  id       INTEGER PRIMARY KEY,
  board_id INTEGER NOT NULL REFERENCES boards(id),
  name     TEXT    NOT NULL DEFAULT '',
  color    TEXT    NOT NULL
) STRICT;
CREATE INDEX labels_board ON labels(board_id);

CREATE TABLE card_labels (
  card_id  INTEGER NOT NULL REFERENCES cards(id),
  label_id INTEGER NOT NULL REFERENCES labels(id) ON DELETE CASCADE,
  PRIMARY KEY (card_id, label_id)
) STRICT, WITHOUT ROWID;
CREATE INDEX card_labels_label ON card_labels(label_id);

CREATE TABLE checklists (
  id      INTEGER PRIMARY KEY,
  card_id INTEGER NOT NULL REFERENCES cards(id),
  title   TEXT    NOT NULL,
  rank    TEXT    NOT NULL
) STRICT;
CREATE INDEX checklists_card ON checklists(card_id, rank);

CREATE TABLE check_items (
  id           INTEGER PRIMARY KEY,
  checklist_id INTEGER NOT NULL REFERENCES checklists(id) ON DELETE CASCADE,
  text         TEXT    NOT NULL,
  done         INTEGER NOT NULL DEFAULT 0,
  rank         TEXT    NOT NULL
) STRICT;
CREATE INDEX check_items_checklist ON check_items(checklist_id, rank);

CREATE TABLE comments (
  id        INTEGER PRIMARY KEY,
  card_id   INTEGER NOT NULL REFERENCES cards(id),
  user_id   INTEGER NOT NULL REFERENCES users(id),
  body      TEXT    NOT NULL,  -- Markdown source
  body_html TEXT    NOT NULL,  -- sanitized render of body
  created   INTEGER NOT NULL,
  edited    INTEGER
) STRICT;
CREATE INDEX comments_card ON comments(card_id, created);

-- Activity feed and SSE replay log in one.
CREATE TABLE events (
  seq       INTEGER PRIMARY KEY,
  board_id  INTEGER NOT NULL REFERENCES boards(id),
  user_id   INTEGER NOT NULL REFERENCES users(id),
  client_id TEXT    NOT NULL DEFAULT '',
  type      TEXT    NOT NULL,
  data      TEXT    NOT NULL,  -- JSON
  created   INTEGER NOT NULL
) STRICT;
CREATE INDEX events_board_seq ON events(board_id, seq);

-- Until login exists, every request acts as this user.
INSERT INTO users (id, email, name, created) VALUES (1, 'dev@localhost', 'Dev User', unixepoch() * 1000);
INSERT INTO workspaces (id, name, created) VALUES (1, 'Default', unixepoch() * 1000);
