package litelog

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// DB is the top-level handle to a litelog database.
type DB struct {
	pool   *sqlitex.Pool
	schema *SchemaCache // will hold interned attribute lookups
	udfs   *udfRegistry

	mu          sync.RWMutex
	nextTx      int64
	nextEID     int64
	udfsDirty   atomic.Bool
	historyPath string // non-empty when hot/cold partitioning is enabled
}

// Options configures a litelog database.
type Options struct {
	PoolSize    int    // number of connections (default 10)
	HistoryPath string // separate file for history datoms (hot/cold partitioning)
}

// Open opens or creates a litelog database at the given path.
func Open(path string, opts ...Options) (*DB, error) {
	o := Options{PoolSize: 10}
	if len(opts) > 0 {
		o = opts[0]
	}
	if o.PoolSize <= 0 {
		o.PoolSize = 10
	}

	historyPath := o.HistoryPath

	pool, err := sqlitex.NewPool(path, sqlitex.PoolOptions{
		PoolSize: o.PoolSize,
		Flags:    sqlite.OpenReadWrite | sqlite.OpenCreate | sqlite.OpenWAL | sqlite.OpenURI,
		PrepareConn: func(conn *sqlite.Conn) error {
			if err := initPragmas(conn); err != nil {
				return err
			}
			if historyPath != "" {
				return attachHistory(conn, historyPath)
			}
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("litelog: open pool: %w", err)
	}

	// Initialize schema on first connection
	conn, err := pool.Take(context.Background())
	if err != nil {
		pool.Close()
		return nil, fmt.Errorf("litelog: take conn: %w", err)
	}

	if err := initSchema(conn); err != nil {
		pool.Put(conn)
		pool.Close()
		return nil, fmt.Errorf("litelog: init schema: %w", err)
	}

	if historyPath != "" {
		if err := initHistorySchema(conn); err != nil {
			pool.Put(conn)
			pool.Close()
			return nil, fmt.Errorf("litelog: init history schema: %w", err)
		}
	}

	db := &DB{
		pool:        pool,
		schema:      newSchemaCache(),
		udfs:        newUDFRegistry(),
		historyPath: historyPath,
	}

	// Load existing state
	if err := db.loadState(conn); err != nil {
		pool.Put(conn)
		pool.Close()
		return nil, fmt.Errorf("litelog: load state: %w", err)
	}

	pool.Put(conn)
	return db, nil
}

// Partitioned returns true if hot/cold partitioning is enabled.
func (db *DB) Partitioned() bool {
	return db.historyPath != ""
}

// Close closes all connections in the pool.
func (db *DB) Close() error {
	return db.pool.Close()
}

// withConn takes a connection from the pool, runs fn, and returns it.
func (db *DB) withConn(ctx context.Context, fn func(conn *sqlite.Conn) error) error {
	conn, err := db.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer db.pool.Put(conn)

	if db.udfsDirty.Load() {
		if err := db.installUDFs(conn); err != nil {
			return fmt.Errorf("install UDFs: %w", err)
		}
	}

	return fn(conn)
}

// withWriteConn takes a connection and wraps fn in an immediate transaction.
func (db *DB) withWriteConn(ctx context.Context, fn func(conn *sqlite.Conn) error) error {
	conn, err := db.pool.Take(ctx)
	if err != nil {
		return err
	}
	defer db.pool.Put(conn)

	if db.udfsDirty.Load() {
		if err := db.installUDFs(conn); err != nil {
			return fmt.Errorf("install UDFs: %w", err)
		}
	}

	endFn, err := sqlitex.ImmediateTransaction(conn)
	if err != nil {
		return err
	}
	defer endFn(&err)

	return fn(conn)
}

// datomsTable returns the qualified table name for the datoms history table.
// When partitioned, returns "history.datoms"; otherwise "datoms".
func (db *DB) datomsTable() string {
	if db.historyPath != "" {
		return "history.datoms"
	}
	return "datoms"
}

// loadState reads the current max tx ID and max entity ID from the database.
func (db *DB) loadState(conn *sqlite.Conn) error {
	// Get max transaction ID
	stmt, err := conn.Prepare("SELECT COALESCE(MAX(id), 0) FROM transactions")
	if err != nil {
		return err
	}
	if hasRow, err := stmt.Step(); err != nil {
		return err
	} else if hasRow {
		db.nextTx = stmt.ColumnInt64(0) + 1
	}
	stmt.Reset()

	// Get max entity ID (from datoms)
	stmt2, err := conn.Prepare("SELECT COALESCE(MAX(e), 0) FROM current_datoms")
	if err != nil {
		return err
	}
	if hasRow, err := stmt2.Step(); err != nil {
		return err
	} else if hasRow {
		db.nextEID = stmt2.ColumnInt64(0) + 1
	}
	stmt2.Reset()

	// Also check attribute IDs
	stmt3, err := conn.Prepare("SELECT COALESCE(MAX(id), 0) FROM attributes")
	if err != nil {
		return err
	}
	if hasRow, err := stmt3.Step(); err != nil {
		return err
	} else if hasRow {
		maxAttr := stmt3.ColumnInt64(0)
		if maxAttr >= db.nextEID {
			db.nextEID = maxAttr + 1
		}
	}
	stmt3.Reset()

	if db.nextTx == 0 {
		db.nextTx = 1
	}
	if db.nextEID == 0 {
		db.nextEID = 1000 // reserve IDs below 1000 for system use
	}

	return db.loadAttributes(conn)
}

// allocEntityID returns the next available entity ID (thread-safe).
func (db *DB) allocEntityID() int64 {
	db.mu.Lock()
	id := db.nextEID
	db.nextEID++
	db.mu.Unlock()
	return id
}

// allocTxID returns the next transaction ID (thread-safe).
func (db *DB) allocTxID() int64 {
	db.mu.Lock()
	id := db.nextTx
	db.nextTx++
	db.mu.Unlock()
	return id
}
