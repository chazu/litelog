package litelog

import (
	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

const schemaSQL = `
-- Append-only datom log (full history)
CREATE TABLE IF NOT EXISTS datoms (
    e         INTEGER NOT NULL,
    a         INTEGER NOT NULL,
    v         BLOB    NOT NULL,
    v_type    INTEGER NOT NULL,
    v_int     INTEGER,
    v_text    TEXT,
    v_float   REAL,
    tx        INTEGER NOT NULL,
    added     INTEGER NOT NULL DEFAULT 1,
    valid_from INTEGER NOT NULL,
    valid_to   INTEGER,
    UNIQUE(e, a, v, tx, added, valid_from)
);

-- Materialized current state (fast path)
CREATE TABLE IF NOT EXISTS current_datoms (
    e         INTEGER NOT NULL,
    a         INTEGER NOT NULL,
    v         BLOB    NOT NULL,
    v_type    INTEGER NOT NULL,
    v_int     INTEGER,
    v_text    TEXT,
    v_float   REAL,
    tx        INTEGER NOT NULL,
    valid_from INTEGER NOT NULL,
    valid_to   INTEGER,
    PRIMARY KEY (e, a, v)
) WITHOUT ROWID;

-- Transaction log
CREATE TABLE IF NOT EXISTS transactions (
    id        INTEGER PRIMARY KEY,
    tx_time   INTEGER NOT NULL
);

-- Attribute registry (interned)
CREATE TABLE IF NOT EXISTS attributes (
    id          INTEGER PRIMARY KEY,
    ident       TEXT    NOT NULL UNIQUE,
    val_type    INTEGER NOT NULL,
    cardinality INTEGER NOT NULL DEFAULT 1,
    indexed     INTEGER NOT NULL DEFAULT 0,
    unique_val  INTEGER NOT NULL DEFAULT 0,
    is_component INTEGER NOT NULL DEFAULT 0
);

-- EAVT covering index (entity-centric access)
CREATE INDEX IF NOT EXISTS eavt ON current_datoms (e, a, v, tx);

-- AEVT covering index (attribute-centric access)
CREATE INDEX IF NOT EXISTS aevt ON current_datoms (a, e, v, tx);

-- AVET partial index (value lookup for indexed attributes)
CREATE INDEX IF NOT EXISTS avet ON current_datoms (a, v, e, tx);

-- VAET index for ref-type attributes (reverse navigation)
CREATE INDEX IF NOT EXISTS vaet ON current_datoms (v, a, e, tx)
    WHERE v_type = 5;

-- Transaction-time access on history
CREATE INDEX IF NOT EXISTS idx_datoms_tx ON datoms (tx);

-- Bitemporal range queries on history
CREATE INDEX IF NOT EXISTS idx_datoms_temporal ON datoms (e, a, valid_from, tx);

-- EAVT on history for as-of queries
CREATE INDEX IF NOT EXISTS idx_datoms_eavt ON datoms (e, a, v, tx);
`

// initSchema creates all tables and indexes, then runs migrations.
func initSchema(conn *sqlite.Conn) error {
	if err := sqlitex.ExecuteScript(conn, schemaSQL, nil); err != nil {
		return err
	}
	return migrateTypedColumns(conn)
}

// migrateTypedColumns adds v_int/v_text/v_float columns to existing databases
// that were created before type-specialized columns existed.
func migrateTypedColumns(conn *sqlite.Conn) error {
	migrations := []string{
		"ALTER TABLE datoms ADD COLUMN v_int INTEGER",
		"ALTER TABLE datoms ADD COLUMN v_text TEXT",
		"ALTER TABLE datoms ADD COLUMN v_float REAL",
		"ALTER TABLE current_datoms ADD COLUMN v_int INTEGER",
		"ALTER TABLE current_datoms ADD COLUMN v_text TEXT",
		"ALTER TABLE current_datoms ADD COLUMN v_float REAL",
	}
	for _, sql := range migrations {
		// Ignore "duplicate column" errors for idempotency.
		_ = sqlitex.ExecuteTransient(conn, sql, nil)
	}
	return nil
}

// attachHistory ATTACHes the history database on a connection.
func attachHistory(conn *sqlite.Conn, path string) error {
	stmt, err := conn.Prepare("ATTACH DATABASE $path AS history")
	if err != nil {
		return err
	}
	stmt.SetText("$path", path)
	_, err = stmt.Step()
	stmt.Reset()
	return err
}

// historySchemaSQL creates the datoms table and indexes in the attached history DB.
const historySchemaSQL = `
CREATE TABLE IF NOT EXISTS history.datoms (
    e         INTEGER NOT NULL,
    a         INTEGER NOT NULL,
    v         BLOB    NOT NULL,
    v_type    INTEGER NOT NULL,
    v_int     INTEGER,
    v_text    TEXT,
    v_float   REAL,
    tx        INTEGER NOT NULL,
    added     INTEGER NOT NULL DEFAULT 1,
    valid_from INTEGER NOT NULL,
    valid_to   INTEGER,
    UNIQUE(e, a, v, tx, added, valid_from)
);
CREATE INDEX IF NOT EXISTS history.idx_datoms_tx ON datoms (tx);
CREATE INDEX IF NOT EXISTS history.idx_datoms_temporal ON datoms (e, a, valid_from, tx);
CREATE INDEX IF NOT EXISTS history.idx_datoms_eavt ON datoms (e, a, v, tx);
`

func initHistorySchema(conn *sqlite.Conn) error {
	return sqlitex.ExecuteScript(conn, historySchemaSQL, nil)
}

// initPragmas sets performance-tuned SQLite pragmas.
func initPragmas(conn *sqlite.Conn) error {
	pragmas := []string{
		"PRAGMA journal_mode=wal;",
		"PRAGMA synchronous=NORMAL;",
		"PRAGMA cache_size=-64000;",
		"PRAGMA mmap_size=268435456;",
		"PRAGMA temp_store=MEMORY;",
		"PRAGMA foreign_keys=ON;",
	}
	for _, p := range pragmas {
		if err := sqlitex.ExecuteTransient(conn, p, nil); err != nil {
			return err
		}
	}
	return nil
}
