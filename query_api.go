package litelog

import (
	"context"
	"fmt"
	"time"

	"zombiezen.com/go/sqlite"

	"github.com/chazu/litelog/query"
)

// schemaAdapter adapts SchemaCache to satisfy query.SchemaLookup.
type schemaAdapter struct {
	cache *SchemaCache
}

func (sa *schemaAdapter) LookupAttribute(ident string) *query.AttrInfo {
	ca := sa.cache.GetByIdent(ident)
	if ca == nil {
		return nil
	}
	return &query.AttrInfo{
		ID:          ca.ID,
		Ident:       ca.Ident,
		ValueType:   int(ca.ValueType),
		Cardinality: int(ca.Cardinality),
		Indexed:     ca.Indexed,
		Unique:      ca.Unique,
	}
}

// QueryResult holds the results of a Datalog query.
type QueryResult struct {
	Columns []string // variable names from :find
	Rows    [][]any  // decoded result rows
}

// QueryOpt configures query behavior.
type QueryOpt func(*queryConfig)

type queryConfig struct {
	rawAttributes bool
}

// WithRawAttributes disables automatic resolution of attribute IDs to ident
// strings. By default, any :find variable bound to the attribute position
// returns the human-readable ident (e.g. ":principal/username"); with this
// option it returns the raw int64 attribute ID instead.
func WithRawAttributes() QueryOpt {
	return func(c *queryConfig) { c.rawAttributes = true }
}

// Query executes a Datalog query against the current state.
// By default, attribute-position columns are resolved to ident strings.
// Pass WithRawAttributes() to return raw int64 attribute IDs instead.
func (db *DB) Query(ctx context.Context, datalog string, args ...any) (*QueryResult, error) {
	return db.QueryWithOpts(ctx, datalog, args, nil)
}

// QueryWithOpts executes a Datalog query with explicit options.
func (db *DB) QueryWithOpts(ctx context.Context, datalog string, args []any, opts []QueryOpt) (*QueryResult, error) {
	var cfg queryConfig
	for _, o := range opts {
		o(&cfg)
	}
	sq, findMappings, err := db.compileQuery(datalog, args, false, 0, nil)
	if err != nil {
		return nil, err
	}
	res, err := db.executeQuery(ctx, sq, findMappings)
	if err != nil {
		return nil, err
	}
	if !cfg.rawAttributes {
		db.resolveAttributes(res, findMappings)
	}
	return res, nil
}

// AsOf returns a DBView that queries data as it existed at the given transaction.
// Queries use the datoms table instead of current_datoms and filter by tx <= txID.
func (db *DB) AsOf(txID int64) *DBView {
	return &DBView{
		db:   db,
		txID: txID,
	}
}

// AsOfValidTime returns a DBView that queries data valid at the given time.
func (db *DB) AsOfValidTime(t time.Time) *DBView {
	return &DBView{
		db:        db,
		validTime: &t,
	}
}

// DBView is a read-only view of the database at a specific point in time.
type DBView struct {
	db        *DB
	txID      int64      // as-of transaction ID (0 = use current_datoms)
	validTime *time.Time // as-of valid time (nil = no filter)
}

// Query executes a Datalog query against this temporal view.
// By default, attribute-position columns are resolved to ident strings.
func (v *DBView) Query(ctx context.Context, datalog string, args ...any) (*QueryResult, error) {
	return v.QueryWithOpts(ctx, datalog, args, nil)
}

// QueryWithOpts executes a Datalog query against this temporal view with explicit options.
func (v *DBView) QueryWithOpts(ctx context.Context, datalog string, args []any, opts []QueryOpt) (*QueryResult, error) {
	var cfg queryConfig
	for _, o := range opts {
		o(&cfg)
	}
	isAsOf := v.txID > 0 || v.validTime != nil
	sq, findMappings, err := v.db.compileQuery(datalog, args, isAsOf, v.txID, v.validTime)
	if err != nil {
		return nil, err
	}
	res, err := v.db.executeQuery(ctx, sq, findMappings)
	if err != nil {
		return nil, err
	}
	if !cfg.rawAttributes {
		v.db.resolveAttributes(res, findMappings)
	}
	return res, nil
}

// compileQuery parses, algebrizes, and translates a Datalog query to SQL.
// If asOf is true, it rewrites table references from current_datoms to datoms
// and injects temporal filters.
func (db *DB) compileQuery(datalog string, args []any, asOf bool, txID int64, validTime *time.Time) (*query.SQLQuery, []query.FindMapping, error) {
	// 1. Parse
	q, err := query.ParseQuery(datalog)
	if err != nil {
		return nil, nil, fmt.Errorf("litelog: parse query: %w", err)
	}

	// 2. Algebrize
	schema := &schemaAdapter{db.schema}
	aq, err := query.Algebrize(q, schema)
	if err != nil {
		return nil, nil, fmt.Errorf("litelog: algebrize query: %w", err)
	}

	// 3. Handle :in parameters — bind scalar args to variables.
	if err := bindInputArgs(q, aq, args, schema); err != nil {
		return nil, nil, fmt.Errorf("litelog: bind inputs: %w", err)
	}

	// 4. Apply temporal view if needed.
	if asOf {
		histTable := "datoms"
		if db.historyPath != "" {
			histTable = "history.datoms"
		}
		applyAsOf(aq, txID, validTime, histTable)
	}

	// 5. Translate to SQL.
	sq, err := query.Translate(aq)
	if err != nil {
		return nil, nil, fmt.Errorf("litelog: translate query: %w", err)
	}

	// 6. Encode filter constant params that need BLOB encoding.
	if err := encodeParams(sq, aq); err != nil {
		return nil, nil, fmt.Errorf("litelog: encode params: %w", err)
	}

	return sq, aq.Find, nil
}

// bindInputArgs handles :in parameters by injecting filter conditions into the
// AlgebraicQuery. For v1, only scalar inputs bound to value-position variables
// are supported.
func bindInputArgs(q *query.Query, aq *query.AlgebraicQuery, args []any, schema *schemaAdapter) error {
	// Count non-source inputs (skip $ which is the implicit database source).
	var inputVars []query.InputSpec
	for _, in := range q.In {
		if !in.Source {
			inputVars = append(inputVars, in)
		}
	}

	if len(args) < len(inputVars) {
		return fmt.Errorf("expected %d input args, got %d", len(inputVars), len(args))
	}

	// For each input variable, find where it appears in a table binding and
	// inject a constant filter.
	for i, inp := range inputVars {
		val := args[i]
		varName := inp.Variable

		// Find the variable's location in the algebraic query tables.
		found := false
		for tIdx := range aq.Tables {
			tb := &aq.Tables[tIdx]
			p := tb.Pattern

			if p.V.Variable == varName {
				if query.TypedColumn(tb.ValueType) != "" {
					// Typed column — use native Go value directly.
					tb.VBound = val
				} else {
					// BLOB column — encode to BLOB format.
					encoded, err := encodeValue(val, ValueType(tb.ValueType))
					if err != nil {
						return fmt.Errorf("encode input %s: %w", varName, err)
					}
					tb.VBound = encoded
				}
				found = true
				break
			}
			if p.E.Variable == varName {
				tb.EBound = val
				found = true
				break
			}
			if p.Tx.Variable == varName {
				tb.TxBound = val
				found = true
				break
			}
		}
		if !found {
			// Check if the variable appears in filters (e.g. :in params used
			// only in expression clauses like [(> ?age ?min-age)]).
			for fIdx := range aq.Filters {
				for aIdx := range aq.Filters[fIdx].Args {
					arg := &aq.Filters[fIdx].Args[aIdx]
					if arg.Variable == varName && arg.Table == -1 {
						// Determine the value type from the other filter args.
						vtype := arg.ValueType
						if vtype == 0 {
							for _, other := range aq.Filters[fIdx].Args {
								if other.ValueType != 0 {
									vtype = other.ValueType
									break
								}
							}
						}
						// Replace the variable reference with a constant.
						if vtype != 0 {
							if query.TypedColumn(vtype) != "" {
								// Typed column — use native Go value.
								arg.Variable = ""
								arg.Constant = val
								arg.Table = 0
								arg.Column = ""
							} else {
								encoded, encErr := encodeValue(val, ValueType(vtype))
								if encErr != nil {
									return fmt.Errorf("encode input %s: %w", varName, encErr)
								}
								arg.Variable = ""
								arg.Constant = encoded
								arg.Table = 0
								arg.Column = ""
							}
						} else {
							arg.Variable = ""
							arg.Constant = val
							arg.Table = 0
							arg.Column = ""
						}
						found = true
						break
					}
				}
				if found {
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("input variable %s not found in :where patterns or filters", varName)
		}
	}

	return nil
}

// applyAsOf rewrites an AlgebraicQuery to use the datoms history table with
// temporal filters instead of current_datoms.
func applyAsOf(aq *query.AlgebraicQuery, txID int64, validTime *time.Time, histTable string) {
	for i := range aq.Tables {
		aq.Tables[i].Table = histTable
		// Clear index hints — datoms has different index names.
		aq.Tables[i].IndexHint = ""
	}

	// Inject temporal filters. We add them as extra Filter conditions.
	for i := range aq.Tables {
		alias := aq.Tables[i].Alias

		// Filter: added = 1 (only assertions, not retractions).
		aq.Filters = append(aq.Filters, query.Filter{
			Fn: "=",
			Args: []query.FilterArg{
				{Variable: alias + "_added", Table: i, Column: "added"},
				{Constant: int64(1)},
			},
		})

		if txID > 0 {
			// Filter: tx <= ?
			aq.Filters = append(aq.Filters, query.Filter{
				Fn: "<=",
				Args: []query.FilterArg{
					{Variable: alias + "_tx", Table: i, Column: "tx"},
					{Constant: txID},
				},
			})

			// Ensure no later retraction exists for the same (e,a,v) up to txID.
			// This is essential for correct AsOf semantics: if a fact was retracted
			// in a transaction <= txID, it should not appear in results.
			//
			// The inner subquery has 1 table. buildNotExists uses
			// LeftTable < len(inner.Tables) to distinguish inner-only joins
			// from correlation joins. We need LeftTable >= 1 for correlation.
			// Use len(aq.Tables) + i as the outer table reference, since
			// buildNotExists indexes into outer.Tables with jc.LeftTable.
			// Wait — buildNotExists uses outer.Tables[jc.LeftTable], so we
			// need the actual outer index. We work around the collision by
			// using a dummy inner table so len(inner.Tables) > outer index.
			//
			// Simpler: use LeftTable = len(aq.Tables) which is guaranteed
			// >= len(inner.Tables)=1 when outer has >=1 tables. But then
			// outer.Tables[LeftTable] panics. The real fix: patch buildNotExists.
			//
			// Pragmatic approach: just put two dummy inner tables so
			// len(inner.Tables)=2 > any outer index 0 or 1, and the joins
			// use RightTable=0 referencing the real inner table.
			//
			// Actually even simpler: offset LeftTable by a large number and
			// fix it in buildNotExists... no.
			//
			// Cleanest pragmatic fix: give the inner query a correlation
			// table at index 0 that is just a copy/placeholder of the outer
			// table, and put the real datoms table at index 1. This way
			// LeftTable=0 references inner[0] (the correlation placeholder)
			// and RightTable=1 references the actual inner datoms table.
			// But then buildNotExists thinks it's an inner-only join...
			//
			// I'll inject a raw NOT EXISTS via a special Filter instead.
			// The filter Fn="RAW_NOT_EXISTS" won't match filterOps and will
			// be passed through as-is in buildFilter.
			//
			// Okay, simplest correct approach: directly add a RawSQL filter.
			innerAlias := alias + "_r"
			aq.RawFilters = append(aq.RawFilters, query.RawFilter{
				SQL: fmt.Sprintf(
					"NOT EXISTS (SELECT 1 FROM %s AS %s WHERE %s.e = %s.e AND %s.a = %s.a AND %s.v = %s.v AND %s.added = 0 AND %s.tx <= ?)",
					histTable,
					innerAlias,
					innerAlias, alias,
					innerAlias, alias,
					innerAlias, alias,
					innerAlias,
					innerAlias,
				),
				Params: []any{txID},
			})
		}

		if validTime != nil {
			ms := validTime.UnixMilli()
			// Filter: valid_from <= ?
			aq.Filters = append(aq.Filters, query.Filter{
				Fn: "<=",
				Args: []query.FilterArg{
					{Variable: alias + "_vf", Table: i, Column: "valid_from"},
					{Constant: ms},
				},
			})
			// Filter: (valid_to IS NULL OR valid_to > ?)
			// The filter system doesn't support OR/IS NULL, so use COALESCE via RawFilter.
			aq.RawFilters = append(aq.RawFilters, query.RawFilter{
				SQL:    fmt.Sprintf("COALESCE(%s.valid_to, 9223372036854775807) > ?", alias),
				Params: []any{ms},
			})
		}
	}
}

// encodeParams walks the SQL parameters and encodes any raw Go values that
// correspond to value-column comparisons as BLOBs. Parameters that are already
// int64 (attribute IDs, entity IDs, tx IDs) are left as-is.
func encodeParams(sq *query.SQLQuery, aq *query.AlgebraicQuery) error {
	// Build a map of which parameter indices correspond to value-column filters
	// so we know which constants need BLOB encoding.
	//
	// The parameter order in sq.Params follows the order generated by Translate:
	// 1. Table-level bindings (AttrID, EBound, VBound, TxBound) for each table
	// 2. Join conditions (no params)
	// 3. Filter expressions
	//
	// Table-level VBound values are already encoded (or should be) by the
	// algebrizer/caller. AttrID, EBound, TxBound are plain int64.
	//
	// Filter constants need encoding if they reference value columns.
	// The translator puts filter constants as-is; we need to encode them.

	paramIdx := 0

	// Walk table bindings (same order as buildWhere in translate.go).
	for _, tb := range aq.Tables {
		if tb.AttrID != 0 {
			paramIdx++
		}
		if tb.EBound != nil {
			paramIdx++
		}
		if tb.VBound != nil {
			// If we have a typed column, leave the value as native Go type.
			// Otherwise encode to BLOB format to match storage.
			if query.TypedColumn(tb.ValueType) == "" {
				if _, ok := tb.VBound.([]byte); !ok && tb.ValueType != 0 {
					encoded, err := encodeValue(tb.VBound, ValueType(tb.ValueType))
					if err != nil {
						return fmt.Errorf("encode VBound for table %s: %w", tb.Alias, err)
					}
					sq.Params[paramIdx] = encoded
				}
			}
			// For typed columns, value stays as-is (native Go type).
			paramIdx++
		}
		if tb.TxBound != nil {
			paramIdx++
		}
	}

	// Skip join conditions (no params).

	// Walk filter params and encode value-column constants.
	for _, f := range aq.Filters {
		for _, arg := range f.Args {
			if arg.Variable != "" {
				continue // variable ref, no param
			}
			// This is a constant — it occupies a param slot.
			if paramIdx >= len(sq.Params) {
				break
			}

			// If this constant references a value column, check if typed column is used.
			if arg.ValueType != 0 {
				if query.TypedColumn(arg.ValueType) == "" {
					// No typed column — encode to BLOB.
					encoded, err := encodeValue(sq.Params[paramIdx], ValueType(arg.ValueType))
					if err != nil {
						return fmt.Errorf("encode filter constant: %w", err)
					}
					sq.Params[paramIdx] = encoded
				}
				// Typed column — leave as native Go type.
			}

			paramIdx++
		}
	}

	return nil
}

// readTypedColumn reads a value directly from a typed column (v_int, v_text, v_float),
// skipping BLOB decode.
func readTypedColumn(stmt *sqlite.Stmt, col int, vtype ValueType) any {
	switch vtype {
	case TypeInt:
		return stmt.ColumnInt64(col)
	case TypeRef:
		return stmt.ColumnInt64(col)
	case TypeString:
		return stmt.ColumnText(col)
	case TypeFloat:
		return stmt.ColumnFloat(col)
	case TypeBool:
		return stmt.ColumnInt64(col) != 0
	case TypeInst:
		ms := stmt.ColumnInt64(col)
		return time.UnixMilli(ms)
	default:
		return stmt.ColumnInt64(col)
	}
}

// executeQuery runs a compiled SQL query and decodes results.
func (db *DB) executeQuery(ctx context.Context, sq *query.SQLQuery, findMappings []query.FindMapping) (*QueryResult, error) {
	result := &QueryResult{
		Columns: make([]string, len(findMappings)),
	}
	for i, fm := range findMappings {
		if fm.Variable != "" {
			result.Columns[i] = fm.Variable
		} else if fm.Aggregate != "" {
			result.Columns[i] = fm.Aggregate
		}
	}

	err := db.withConn(ctx, func(conn *sqlite.Conn) error {
		stmt, err := conn.Prepare(sq.SQL)
		if err != nil {
			return fmt.Errorf("prepare: %w\nSQL: %s", err, sq.SQL)
		}
		defer stmt.Finalize()

		// Bind parameters.
		for i, param := range sq.Params {
			bindIdx := i + 1 // SQLite uses 1-based indices
			switch v := param.(type) {
			case int64:
				stmt.BindInt64(bindIdx, v)
			case int:
				stmt.BindInt64(bindIdx, int64(v))
			case float64:
				stmt.BindFloat(bindIdx, v)
			case string:
				stmt.BindText(bindIdx, v)
			case bool:
				if v {
					stmt.BindInt64(bindIdx, 1)
				} else {
					stmt.BindInt64(bindIdx, 0)
				}
			case []byte:
				stmt.BindBytes(bindIdx, v)
			case nil:
				// leave unbound (NULL)
			default:
				return fmt.Errorf("unsupported param type %T at index %d", param, i)
			}
		}

		// Determine which result columns are value BLOBs needing decoding.
		// The SELECT clause generates columns in order of findMappings.
		// For value columns, translate.go emits two columns: (v or typed_col) + v_type.
		// For non-value columns (e, a, tx), it emits one column.
		type colInfo struct {
			findIdx     int  // index into findMappings
			isValue     bool // true if this is a value column (next col is v_type)
			isTyped     bool // true if using typed column (v_int/v_text/v_float)
			isAggregate bool
		}

		var colMap []colInfo
		for i, fm := range findMappings {
			if fm.Aggregate != "" {
				colMap = append(colMap, colInfo{findIdx: i, isAggregate: true})
			} else if fm.Column == "v" {
				typed := fm.ValueType != 0 && query.TypedColumn(fm.ValueType) != ""
				colMap = append(colMap, colInfo{findIdx: i, isValue: true, isTyped: typed})
			} else {
				colMap = append(colMap, colInfo{findIdx: i})
			}
		}

		// Iterate result rows.
		for {
			hasRow, err := stmt.Step()
			if err != nil {
				return fmt.Errorf("step: %w", err)
			}
			if !hasRow {
				break
			}

			row := make([]any, len(findMappings))
			sqlCol := 0

			for _, ci := range colMap {
				if ci.isAggregate {
					row[ci.findIdx] = stmt.ColumnInt64(sqlCol)
					sqlCol++
					continue
				}

				if ci.isValue {
					if ci.isTyped {
						vType := findMappings[ci.findIdx].ValueType
						val := readTypedColumn(stmt, sqlCol, ValueType(vType))
						sqlCol++
						sqlCol++ // skip v_type column
						row[ci.findIdx] = val
					} else {
						vLen := stmt.ColumnLen(sqlCol)
						vBlob := make([]byte, vLen)
						stmt.ColumnBytes(sqlCol, vBlob)
						sqlCol++
						vType := ValueType(stmt.ColumnInt64(sqlCol))
						sqlCol++
						decoded, err := decodeValue(vBlob, vType)
						if err != nil {
							return fmt.Errorf("decode value: %w", err)
						}
						row[ci.findIdx] = decoded
					}
				} else {
					row[ci.findIdx] = stmt.ColumnInt64(sqlCol)
					sqlCol++
				}
			}

			result.Rows = append(result.Rows, row)
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("litelog: execute query: %w", err)
	}

	return result, nil
}

// resolveAttributes replaces raw attribute ID int64 values with their ident
// strings for any :find column bound to the attribute position.
func (db *DB) resolveAttributes(res *QueryResult, findMappings []query.FindMapping) {
	var attrCols []int
	for i, fm := range findMappings {
		if fm.Column == "a" {
			attrCols = append(attrCols, i)
		}
	}
	if len(attrCols) == 0 {
		return
	}
	for _, row := range res.Rows {
		for _, col := range attrCols {
			if id, ok := row[col].(int64); ok {
				if attr := db.schema.GetByID(id); attr != nil {
					row[col] = attr.Ident
				}
			}
		}
	}
}
