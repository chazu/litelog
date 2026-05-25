package litelog

import (
	"context"
	"fmt"
	"sync"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// SchemaCache is an in-memory cache of attribute definitions for fast lookup.
type SchemaCache struct {
	mu      sync.RWMutex
	byIdent map[string]*CachedAttribute
	byID    map[int64]*CachedAttribute
}

// CachedAttribute is an attribute definition cached in memory.
type CachedAttribute struct {
	ID          int64
	Ident       string
	ValueType   ValueType
	Cardinality Cardinality
	Indexed     bool
	Unique      bool
	IsComponent bool
}

func newSchemaCache() *SchemaCache {
	return &SchemaCache{
		byIdent: make(map[string]*CachedAttribute),
		byID:    make(map[int64]*CachedAttribute),
	}
}

// GetByIdent returns the cached attribute for the given ident, or nil.
func (sc *SchemaCache) GetByIdent(ident string) *CachedAttribute {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.byIdent[ident]
}

// GetByID returns the cached attribute for the given ID, or nil.
func (sc *SchemaCache) GetByID(id int64) *CachedAttribute {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	return sc.byID[id]
}

// put adds or updates an attribute in the cache.
func (sc *SchemaCache) put(attr *CachedAttribute) {
	sc.mu.Lock()
	sc.byIdent[attr.Ident] = attr
	sc.byID[attr.ID] = attr
	sc.mu.Unlock()
}

// All returns all cached attributes.
func (sc *SchemaCache) All() []*CachedAttribute {
	sc.mu.RLock()
	defer sc.mu.RUnlock()
	attrs := make([]*CachedAttribute, 0, len(sc.byID))
	for _, a := range sc.byID {
		attrs = append(attrs, a)
	}
	return attrs
}

// Schema returns the schema cache for direct attribute lookups.
func (db *DB) Schema() *SchemaCache { return db.schema }

// DefineAttribute registers a new attribute in the schema.
// If the attribute already exists with the same definition, this is a no-op.
// If it exists with a different definition, returns an error.
func (db *DB) DefineAttribute(ctx context.Context, schema AttributeSchema) error {
	if existing := db.schema.GetByIdent(schema.Ident); existing != nil {
		if existing.ValueType == schema.ValueType &&
			existing.Cardinality == schema.Cardinality &&
			existing.Indexed == schema.Indexed &&
			existing.Unique == schema.Unique &&
			existing.IsComponent == schema.IsComponent {
			return nil // identical, no-op
		}
		return fmt.Errorf("litelog: attribute %q already exists with different definition", schema.Ident)
	}

	attrID := db.allocEntityID()

	err := db.withWriteConn(ctx, func(conn *sqlite.Conn) error {
		stmt, err := conn.Prepare(`INSERT INTO attributes (id, ident, val_type, cardinality, indexed, unique_val, is_component) VALUES ($id, $ident, $vtype, $card, $idx, $uniq, $comp)`)
		if err != nil {
			return err
		}
		stmt.SetInt64("$id", attrID)
		stmt.SetText("$ident", schema.Ident)
		stmt.SetInt64("$vtype", int64(schema.ValueType))
		stmt.SetInt64("$card", int64(schema.Cardinality))
		stmt.SetBool("$idx", schema.Indexed)
		stmt.SetBool("$uniq", schema.Unique)
		stmt.SetBool("$comp", schema.IsComponent)
		if _, err := stmt.Step(); err != nil {
			return err
		}
		stmt.Reset()
		return nil
	})
	if err != nil {
		return fmt.Errorf("litelog: define attribute %q: %w", schema.Ident, err)
	}

	db.schema.put(&CachedAttribute{
		ID:          attrID,
		Ident:       schema.Ident,
		ValueType:   schema.ValueType,
		Cardinality: schema.Cardinality,
		Indexed:     schema.Indexed,
		Unique:      schema.Unique,
		IsComponent: schema.IsComponent,
	})

	return nil
}

// loadAttributes reads all attributes from the database into the cache.
// Called during Open.
func (db *DB) loadAttributes(conn *sqlite.Conn) error {
	return sqlitex.Execute(conn,
		"SELECT id, ident, val_type, cardinality, indexed, unique_val, is_component FROM attributes",
		&sqlitex.ExecOptions{
			ResultFunc: func(stmt *sqlite.Stmt) error {
				db.schema.put(&CachedAttribute{
					ID:          stmt.ColumnInt64(0),
					Ident:       stmt.ColumnText(1),
					ValueType:   ValueType(stmt.ColumnInt64(2)),
					Cardinality: Cardinality(stmt.ColumnInt64(3)),
					Indexed:     stmt.ColumnBool(4),
					Unique:      stmt.ColumnBool(5),
					IsComponent: stmt.ColumnBool(6),
				})
				return nil
			},
		},
	)
}
