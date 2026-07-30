package store

// schemaSQL creates every table, index, and trigger the store relies on. It is
// idempotent (IF NOT EXISTS) so it can run on every open.
//
// chunks_fts is an external-content FTS5 table linked to chunks. The three
// triggers keep it in sync with chunks so BM25 search always reflects the
// current chunk text without storing the text twice.
const schemaSQL = `
CREATE TABLE IF NOT EXISTS meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS files (
	id           INTEGER PRIMARY KEY AUTOINCREMENT,
	path         TEXT NOT NULL UNIQUE,
	mod_time     INTEGER NOT NULL,
	size         INTEGER NOT NULL,
	content_hash TEXT NOT NULL,
	chunk_count  INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS chunks (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	file_id    INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
	start_line INTEGER NOT NULL,
	end_line   INTEGER NOT NULL,
	symbol     TEXT NOT NULL DEFAULT '',
	content    TEXT NOT NULL,
	embedding  BLOB
);

CREATE INDEX IF NOT EXISTS idx_chunks_file ON chunks(file_id);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	content,
	content='chunks',
	content_rowid='id'
);

CREATE TRIGGER IF NOT EXISTS chunks_ai AFTER INSERT ON chunks BEGIN
	INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_ad AFTER DELETE ON chunks BEGIN
	INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
END;

CREATE TRIGGER IF NOT EXISTS chunks_au AFTER UPDATE ON chunks BEGIN
	INSERT INTO chunks_fts(chunks_fts, rowid, content) VALUES ('delete', old.id, old.content);
	INSERT INTO chunks_fts(rowid, content) VALUES (new.id, new.content);
END;
`
