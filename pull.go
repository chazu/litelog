package litelog

import (
	"context"
	"fmt"
	"time"

	"zombiezen.com/go/sqlite"
)

// PullPattern specifies which attributes to retrieve for an entity.
// Use PullWildcard() for all attributes, PullAttrs() for specific ones,
// or build patterns with nested ref traversal.
type PullPattern struct {
	Wildcard bool
	Attrs    []PullAttr
}

// PullAttr specifies a single attribute to pull, optionally with a nested
// pattern for ref-type attributes.
type PullAttr struct {
	Ident   string
	Nested  *PullPattern // follow refs and pull nested entity
	AsAlias string       // rename key in result (empty = use ident)
}

// PullResult is a map of attribute ident to value. For CardMany attributes
// the value is a []any. For nested refs the value is a nested PullResult
// (or []PullResult for CardMany refs).
type PullResult map[string]any

// PullWildcard returns a pattern that pulls all attributes.
func PullWildcard() PullPattern {
	return PullPattern{Wildcard: true}
}

// PullAttrs returns a pattern pulling the named attributes.
func PullAttrs(idents ...string) PullPattern {
	attrs := make([]PullAttr, len(idents))
	for i, id := range idents {
		attrs[i] = PullAttr{Ident: id}
	}
	return PullPattern{Attrs: attrs}
}

// Pull retrieves attribute values for a single entity, shaped by pattern.
func (db *DB) Pull(ctx context.Context, eid int64, pattern PullPattern) (PullResult, error) {
	var result PullResult
	err := db.withConn(ctx, func(conn *sqlite.Conn) error {
		var err error
		result, err = db.pullEntity(conn, eid, pattern, 0)
		return err
	})
	return result, err
}

// PullMany retrieves attribute values for multiple entities.
func (db *DB) PullMany(ctx context.Context, eids []int64, pattern PullPattern) ([]PullResult, error) {
	results := make([]PullResult, len(eids))
	err := db.withConn(ctx, func(conn *sqlite.Conn) error {
		for i, eid := range eids {
			r, err := db.pullEntity(conn, eid, pattern, 0)
			if err != nil {
				return err
			}
			results[i] = r
		}
		return nil
	})
	return results, err
}

const maxPullDepth = 16

func (db *DB) pullEntity(conn *sqlite.Conn, eid int64, pattern PullPattern, depth int) (PullResult, error) {
	if depth > maxPullDepth {
		return nil, fmt.Errorf("litelog: pull depth exceeds %d (circular ref?)", maxPullDepth)
	}

	result := make(PullResult)
	result[":db/id"] = eid

	if pattern.Wildcard {
		return db.pullWildcard(conn, eid, result, pattern, depth)
	}

	return db.pullSpecific(conn, eid, result, pattern, depth)
}

func (db *DB) pullWildcard(conn *sqlite.Conn, eid int64, result PullResult, pattern PullPattern, depth int) (PullResult, error) {
	stmt, err := conn.Prepare("SELECT a, v, v_type, v_int, v_text, v_float FROM current_datoms WHERE e = ?")
	if err != nil {
		return nil, fmt.Errorf("litelog: pull prepare: %w", err)
	}
	defer stmt.Reset()

	stmt.BindInt64(1, eid)

	type rawDatom struct {
		attrID int64
		val    any
	}
	var datoms []rawDatom

	for {
		hasRow, err := stmt.Step()
		if err != nil {
			return nil, fmt.Errorf("litelog: pull step: %w", err)
		}
		if !hasRow {
			break
		}

		attrID := stmt.ColumnInt64(0)
		vType := ValueType(stmt.ColumnInt64(2))

		var decoded any
		decoded = readTypedColumnFromPull(stmt, vType)
		if decoded == nil {
			vLen := stmt.ColumnLen(1)
			vBlob := make([]byte, vLen)
			stmt.ColumnBytes(1, vBlob)
			var err error
			decoded, err = decodeValue(vBlob, vType)
			if err != nil {
				return nil, fmt.Errorf("litelog: pull decode: %w", err)
			}
		}
		datoms = append(datoms, rawDatom{attrID: attrID, val: decoded})
	}

	for _, d := range datoms {
		attr := db.schema.GetByID(d.attrID)
		if attr == nil {
			continue
		}

		val := d.val
		if attr.ValueType == TypeRef {
			if refID, ok := val.(int64); ok {
				nested, err := db.pullEntity(conn, refID, pattern, depth+1)
				if err != nil {
					val = refID
				} else {
					val = nested
				}
			}
		}

		if attr.Cardinality == CardMany {
			existing, ok := result[attr.Ident]
			if ok {
				result[attr.Ident] = append(existing.([]any), val)
			} else {
				result[attr.Ident] = []any{val}
			}
		} else {
			result[attr.Ident] = val
		}
	}

	return result, nil
}

func (db *DB) pullSpecific(conn *sqlite.Conn, eid int64, result PullResult, pattern PullPattern, depth int) (PullResult, error) {
	stmt, err := conn.Prepare("SELECT v, v_type FROM current_datoms WHERE e = ? AND a = ?")
	if err != nil {
		return nil, fmt.Errorf("litelog: pull prepare: %w", err)
	}
	defer stmt.Reset()

	for _, pa := range pattern.Attrs {
		attr := db.schema.GetByIdent(pa.Ident)
		if attr == nil {
			continue
		}

		stmt.Reset()
		stmt.BindInt64(1, eid)
		stmt.BindInt64(2, attr.ID)

		key := pa.Ident
		if pa.AsAlias != "" {
			key = pa.AsAlias
		}

		if attr.Cardinality == CardMany {
			var vals []any
			for {
				hasRow, err := stmt.Step()
				if err != nil {
					return nil, fmt.Errorf("litelog: pull step: %w", err)
				}
				if !hasRow {
					break
				}
				val, err := readValueFromStmt(stmt, 0)
				if err != nil {
					return nil, err
				}
				if pa.Nested != nil && attr.ValueType == TypeRef {
					if refID, ok := val.(int64); ok {
						nested, nerr := db.pullEntity(conn, refID, *pa.Nested, depth+1)
						if nerr == nil {
							val = nested
						}
					}
				}
				vals = append(vals, val)
			}
			if len(vals) > 0 {
				result[key] = vals
			}
		} else {
			hasRow, err := stmt.Step()
			if err != nil {
				return nil, fmt.Errorf("litelog: pull step: %w", err)
			}
			if !hasRow {
				continue
			}
			val, err := readValueFromStmt(stmt, 0)
			if err != nil {
				return nil, err
			}
			if pa.Nested != nil && attr.ValueType == TypeRef {
				if refID, ok := val.(int64); ok {
					nested, nerr := db.pullEntity(conn, refID, *pa.Nested, depth+1)
					if nerr == nil {
						val = nested
					}
				}
			}
			result[key] = val
		}
	}

	return result, nil
}

func readValueFromStmt(stmt *sqlite.Stmt, colOffset int) (any, error) {
	vLen := stmt.ColumnLen(colOffset)
	vBlob := make([]byte, vLen)
	stmt.ColumnBytes(colOffset, vBlob)
	vType := ValueType(stmt.ColumnInt64(colOffset + 1))
	return decodeValue(vBlob, vType)
}

// readTypedColumnFromPull reads from typed columns (v_int at col 3, v_text at col 4,
// v_float at col 5) based on value type. Returns nil if no typed column applies.
func readTypedColumnFromPull(stmt *sqlite.Stmt, vType ValueType) any {
	switch vType {
	case TypeInt:
		return stmt.ColumnInt64(3)
	case TypeRef:
		return stmt.ColumnInt64(3)
	case TypeBool:
		return stmt.ColumnInt64(3) != 0
	case TypeInst:
		return time.UnixMilli(stmt.ColumnInt64(3))
	case TypeString:
		return stmt.ColumnText(4)
	case TypeFloat:
		return stmt.ColumnFloat(5)
	default:
		return nil
	}
}

// Pull on a DBView queries temporal state.
func (v *DBView) Pull(ctx context.Context, eid int64, pattern PullPattern) (PullResult, error) {
	var result PullResult
	err := v.db.withConn(ctx, func(conn *sqlite.Conn) error {
		var err error
		result, err = v.pullEntity(conn, eid, pattern, 0)
		return err
	})
	return result, err
}

func (v *DBView) pullEntity(conn *sqlite.Conn, eid int64, pattern PullPattern, depth int) (PullResult, error) {
	if depth > maxPullDepth {
		return nil, fmt.Errorf("litelog: pull depth exceeds %d", maxPullDepth)
	}

	table, whereExtra, params := v.temporalParams(eid)
	result := make(PullResult)
	result[":db/id"] = eid

	if pattern.Wildcard {
		return v.pullWildcardTemporal(conn, eid, result, pattern, depth, table, whereExtra, params)
	}
	return v.pullSpecificTemporal(conn, eid, result, pattern, depth, table, whereExtra, params)
}

func (v *DBView) temporalParams(eid int64) (table string, whereExtra string, params []any) {
	params = []any{eid}

	histTable := "datoms"
	if v.db.historyPath != "" {
		histTable = "history.datoms"
	}

	if v.txID > 0 {
		table = histTable
		whereExtra = " AND added = 1 AND tx <= ?"
		params = append(params, v.txID)
	} else if v.validTime != nil {
		table = histTable
		ms := v.validTime.UnixMilli()
		whereExtra = " AND added = 1 AND valid_from <= ? AND COALESCE(valid_to, 9223372036854775807) > ?"
		params = append(params, ms, ms)
	} else {
		table = "current_datoms"
		whereExtra = ""
	}
	return
}

func (v *DBView) pullWildcardTemporal(conn *sqlite.Conn, eid int64, result PullResult, pattern PullPattern, depth int, table, whereExtra string, baseParams []any) (PullResult, error) {
	sql := fmt.Sprintf("SELECT a, v, v_type FROM %s WHERE e = ?%s", table, whereExtra)
	stmt, err := conn.Prepare(sql)
	if err != nil {
		return nil, err
	}
	defer stmt.Reset()

	for i, p := range baseParams {
		switch pv := p.(type) {
		case int64:
			stmt.BindInt64(i+1, pv)
		}
	}

	type rawDatom struct {
		attrID int64
		val    any
	}
	var datoms []rawDatom

	for {
		hasRow, err := stmt.Step()
		if err != nil {
			return nil, err
		}
		if !hasRow {
			break
		}
		attrID := stmt.ColumnInt64(0)
		vLen := stmt.ColumnLen(1)
		vBlob := make([]byte, vLen)
		stmt.ColumnBytes(1, vBlob)
		vType := ValueType(stmt.ColumnInt64(2))
		decoded, err := decodeValue(vBlob, vType)
		if err != nil {
			return nil, err
		}
		datoms = append(datoms, rawDatom{attrID: attrID, val: decoded})
	}

	for _, d := range datoms {
		attr := v.db.schema.GetByID(d.attrID)
		if attr == nil {
			continue
		}
		val := d.val
		if attr.ValueType == TypeRef {
			if refID, ok := val.(int64); ok {
				nested, err := v.pullEntity(conn, refID, pattern, depth+1)
				if err == nil {
					val = nested
				}
			}
		}
		if attr.Cardinality == CardMany {
			existing, ok := result[attr.Ident]
			if ok {
				result[attr.Ident] = append(existing.([]any), val)
			} else {
				result[attr.Ident] = []any{val}
			}
		} else {
			result[attr.Ident] = val
		}
	}

	return result, nil
}

func (v *DBView) pullSpecificTemporal(conn *sqlite.Conn, eid int64, result PullResult, pattern PullPattern, depth int, table, whereExtra string, baseParams []any) (PullResult, error) {
	sql := fmt.Sprintf("SELECT v, v_type FROM %s WHERE e = ? AND a = ?%s", table, whereExtra)

	for _, pa := range pattern.Attrs {
		attr := v.db.schema.GetByIdent(pa.Ident)
		if attr == nil {
			continue
		}

		stmt, err := conn.Prepare(sql)
		if err != nil {
			return nil, err
		}

		stmt.BindInt64(1, eid)
		stmt.BindInt64(2, attr.ID)
		// bind extra temporal params starting at position 3
		for i := 1; i < len(baseParams); i++ {
			if pv, ok := baseParams[i].(int64); ok {
				stmt.BindInt64(i+2, pv)
			}
		}

		key := pa.Ident
		if pa.AsAlias != "" {
			key = pa.AsAlias
		}

		if attr.Cardinality == CardMany {
			var vals []any
			for {
				hasRow, err := stmt.Step()
				if err != nil {
					stmt.Reset()
					return nil, err
				}
				if !hasRow {
					break
				}
				val, err := readValueFromStmt(stmt, 0)
				if err != nil {
					stmt.Reset()
					return nil, err
				}
				if pa.Nested != nil && attr.ValueType == TypeRef {
					if refID, ok := val.(int64); ok {
						nested, nerr := v.pullEntity(conn, refID, *pa.Nested, depth+1)
						if nerr == nil {
							val = nested
						}
					}
				}
				vals = append(vals, val)
			}
			if len(vals) > 0 {
				result[key] = vals
			}
		} else {
			hasRow, err := stmt.Step()
			if err != nil {
				stmt.Reset()
				return nil, err
			}
			if hasRow {
				val, err := readValueFromStmt(stmt, 0)
				if err != nil {
					stmt.Reset()
					return nil, err
				}
				if pa.Nested != nil && attr.ValueType == TypeRef {
					if refID, ok := val.(int64); ok {
						nested, nerr := v.pullEntity(conn, refID, *pa.Nested, depth+1)
						if nerr == nil {
							val = nested
						}
					}
				}
				result[key] = val
			}
		}
		stmt.Reset()
	}

	return result, nil
}
