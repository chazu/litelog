package litelog

import (
	"context"
	"fmt"
	"time"

	"zombiezen.com/go/sqlite"
)

// Transact applies a batch of TxDatoms atomically.
// TempIDs are resolved to new entity IDs; CardOne attributes automatically
// retract the previous value before asserting the new one.
func (db *DB) Transact(ctx context.Context, datoms []TxDatom) (*TxResult, error) {
	return db.transact(ctx, datoms, time.Time{})
}

// TransactWithValidTime is like Transact but uses the provided validTime for
// the valid_from field instead of the transaction time.
func (db *DB) TransactWithValidTime(ctx context.Context, datoms []TxDatom, validTime time.Time) (*TxResult, error) {
	return db.transact(ctx, datoms, validTime)
}

// bindTypedColumns sets $vi, $vs, $vf2 on a statement based on value type.
func bindTypedColumns(stmt *sqlite.Stmt, val any, vtype ValueType) {
	stmt.SetNull("$vi")
	stmt.SetNull("$vs")
	stmt.SetNull("$vf2")

	switch vtype {
	case TypeInt, TypeRef:
		if n, ok := val.(int64); ok {
			stmt.SetInt64("$vi", n)
		} else if n, ok := val.(int); ok {
			stmt.SetInt64("$vi", int64(n))
		}
	case TypeString:
		if s, ok := val.(string); ok {
			stmt.SetText("$vs", s)
		}
	case TypeFloat:
		if f, ok := val.(float64); ok {
			stmt.SetFloat("$vf2", f)
		}
	case TypeBool:
		if b, ok := val.(bool); ok {
			if b {
				stmt.SetInt64("$vi", 1)
			} else {
				stmt.SetInt64("$vi", 0)
			}
		}
	case TypeInst:
		if t, ok := val.(time.Time); ok {
			stmt.SetInt64("$vi", t.UnixMilli())
		}
	}
}

func (db *DB) transact(ctx context.Context, txDatoms []TxDatom, validTime time.Time) (*TxResult, error) {
	txID := db.allocTxID()
	txTime := time.Now().UnixMilli()

	validFrom := txTime
	if !validTime.IsZero() {
		validFrom = validTime.UnixMilli()
	}

	tempids := make(map[TempID]int64)
	var produced []Datom

	err := db.withWriteConn(ctx, func(conn *sqlite.Conn) error {
		// Prepare all statements (cached by the connection).
		stmtInsertTx, err := conn.Prepare("INSERT INTO transactions (id, tx_time) VALUES ($id, $time)")
		if err != nil {
			return fmt.Errorf("prepare insert tx: %w", err)
		}

		datomsTable := db.datomsTable()
		stmtInsertDatom, err := conn.Prepare("INSERT INTO " + datomsTable + " (e, a, v, v_type, v_int, v_text, v_float, tx, added, valid_from, valid_to) VALUES ($e, $a, $v, $vt, $vi, $vs, $vf2, $tx, $added, $vf, $vto)")
		if err != nil {
			return fmt.Errorf("prepare insert datom: %w", err)
		}

		stmtUpsertCurrent, err := conn.Prepare("INSERT OR REPLACE INTO current_datoms (e, a, v, v_type, v_int, v_text, v_float, tx, valid_from, valid_to) VALUES ($e, $a, $v, $vt, $vi, $vs, $vf2, $tx, $vf, $vto)")
		if err != nil {
			return fmt.Errorf("prepare upsert current: %w", err)
		}

		stmtDeleteCurrentEA, err := conn.Prepare("DELETE FROM current_datoms WHERE e = $e AND a = $a")
		if err != nil {
			return fmt.Errorf("prepare delete current ea: %w", err)
		}

		stmtDeleteCurrentEAV, err := conn.Prepare("DELETE FROM current_datoms WHERE e = $e AND a = $a AND v = $v")
		if err != nil {
			return fmt.Errorf("prepare delete current eav: %w", err)
		}

		stmtReadCurrent, err := conn.Prepare("SELECT v, v_type, tx, valid_from FROM current_datoms WHERE e = $e AND a = $a")
		if err != nil {
			return fmt.Errorf("prepare read current: %w", err)
		}

		// Insert transaction record.
		stmtInsertTx.SetInt64("$id", txID)
		stmtInsertTx.SetInt64("$time", txTime)
		if _, err := stmtInsertTx.Step(); err != nil {
			return fmt.Errorf("insert tx: %w", err)
		}
		stmtInsertTx.Reset()

		// Process each TxDatom.
		for i, td := range txDatoms {
			// Resolve attribute.
			attr := db.schema.GetByIdent(td.A)
			if attr == nil {
				return fmt.Errorf("datom %d: unknown attribute %q", i, td.A)
			}

			// Resolve entity ID.
			var eid int64
			switch e := td.E.(type) {
			case TempID:
				resolved, ok := tempids[e]
				if !ok {
					resolved = db.allocEntityID()
					tempids[e] = resolved
				}
				eid = resolved
			case int64:
				eid = e
			case int:
				eid = int64(e)
			default:
				return fmt.Errorf("datom %d: unsupported entity type %T", i, td.E)
			}

			// Encode value.
			vBytes, err := encodeValue(td.V, attr.ValueType)
			if err != nil {
				return fmt.Errorf("datom %d: encode value: %w", i, err)
			}

			if td.Retract {
				// --- Retraction ---
				// Delete from current_datoms by (e, a, v).
				stmtDeleteCurrentEAV.SetInt64("$e", eid)
				stmtDeleteCurrentEAV.SetInt64("$a", attr.ID)
				stmtDeleteCurrentEAV.SetBytes("$v", vBytes)
				if _, err := stmtDeleteCurrentEAV.Step(); err != nil {
					return fmt.Errorf("datom %d: delete current eav: %w", i, err)
				}
				stmtDeleteCurrentEAV.Reset()

				// Log retraction in datoms.
				stmtInsertDatom.SetInt64("$e", eid)
				stmtInsertDatom.SetInt64("$a", attr.ID)
				stmtInsertDatom.SetBytes("$v", vBytes)
				stmtInsertDatom.SetInt64("$vt", int64(attr.ValueType))
				bindTypedColumns(stmtInsertDatom, td.V, attr.ValueType)
				stmtInsertDatom.SetInt64("$tx", txID)
				stmtInsertDatom.SetInt64("$added", 0)
				stmtInsertDatom.SetInt64("$vf", validFrom)
				stmtInsertDatom.SetNull("$vto")
				if _, err := stmtInsertDatom.Step(); err != nil {
					return fmt.Errorf("datom %d: insert retraction: %w", i, err)
				}
				stmtInsertDatom.Reset()

				produced = append(produced, Datom{
					E: eid, A: attr.ID, V: td.V, Tx: txID, Added: false,
				})
			} else {
				// --- Assertion ---
				// For CardOne, retract any existing value first.
				if attr.Cardinality == CardOne {
					stmtReadCurrent.SetInt64("$e", eid)
					stmtReadCurrent.SetInt64("$a", attr.ID)
					hasRow, err := stmtReadCurrent.Step()
					if err != nil {
						stmtReadCurrent.Reset()
						return fmt.Errorf("datom %d: read existing: %w", i, err)
					}
					if hasRow {
						// Read existing value for the retraction log entry.
						oldVLen := stmtReadCurrent.ColumnLen(0)
						oldV := make([]byte, oldVLen)
						stmtReadCurrent.ColumnBytes(0, oldV)
						oldVType := ValueType(stmtReadCurrent.ColumnInt64(1))
						_ = oldVType // same as attr.ValueType

						stmtReadCurrent.Reset()

						// Log retraction of old value.
						oldDecoded2, _ := decodeValue(oldV, attr.ValueType)
						stmtInsertDatom.SetInt64("$e", eid)
						stmtInsertDatom.SetInt64("$a", attr.ID)
						stmtInsertDatom.SetBytes("$v", oldV)
						stmtInsertDatom.SetInt64("$vt", int64(attr.ValueType))
						bindTypedColumns(stmtInsertDatom, oldDecoded2, attr.ValueType)
						stmtInsertDatom.SetInt64("$tx", txID)
						stmtInsertDatom.SetInt64("$added", 0)
						stmtInsertDatom.SetInt64("$vf", validFrom)
						stmtInsertDatom.SetNull("$vto")
						if _, err := stmtInsertDatom.Step(); err != nil {
							return fmt.Errorf("datom %d: insert cardone retraction: %w", i, err)
						}
						stmtInsertDatom.Reset()

						// Decode old value for the produced datom.
						oldDecoded, _ := decodeValue(oldV, attr.ValueType)
						produced = append(produced, Datom{
							E: eid, A: attr.ID, V: oldDecoded, Tx: txID, Added: false,
						})

						// Delete from current_datoms.
						stmtDeleteCurrentEA.SetInt64("$e", eid)
						stmtDeleteCurrentEA.SetInt64("$a", attr.ID)
						if _, err := stmtDeleteCurrentEA.Step(); err != nil {
							return fmt.Errorf("datom %d: delete current ea: %w", i, err)
						}
						stmtDeleteCurrentEA.Reset()
					} else {
						stmtReadCurrent.Reset()
					}
				}

				// Insert assertion into datoms log.
				stmtInsertDatom.SetInt64("$e", eid)
				stmtInsertDatom.SetInt64("$a", attr.ID)
				stmtInsertDatom.SetBytes("$v", vBytes)
				stmtInsertDatom.SetInt64("$vt", int64(attr.ValueType))
				bindTypedColumns(stmtInsertDatom, td.V, attr.ValueType)
				stmtInsertDatom.SetInt64("$tx", txID)
				stmtInsertDatom.SetInt64("$added", 1)
				stmtInsertDatom.SetInt64("$vf", validFrom)
				stmtInsertDatom.SetNull("$vto")
				if _, err := stmtInsertDatom.Step(); err != nil {
					return fmt.Errorf("datom %d: insert assertion: %w", i, err)
				}
				stmtInsertDatom.Reset()

				// Insert/replace into current_datoms.
				stmtUpsertCurrent.SetInt64("$e", eid)
				stmtUpsertCurrent.SetInt64("$a", attr.ID)
				stmtUpsertCurrent.SetBytes("$v", vBytes)
				stmtUpsertCurrent.SetInt64("$vt", int64(attr.ValueType))
				bindTypedColumns(stmtUpsertCurrent, td.V, attr.ValueType)
				stmtUpsertCurrent.SetInt64("$tx", txID)
				stmtUpsertCurrent.SetInt64("$vf", validFrom)
				stmtUpsertCurrent.SetNull("$vto")
				if _, err := stmtUpsertCurrent.Step(); err != nil {
					return fmt.Errorf("datom %d: upsert current: %w", i, err)
				}
				stmtUpsertCurrent.Reset()

				produced = append(produced, Datom{
					E: eid, A: attr.ID, V: td.V, Tx: txID, Added: true,
				})
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("litelog: transact: %w", err)
	}

	return &TxResult{
		TxID:    txID,
		TxTime:  txTime,
		Tempids: tempids,
		Datoms:  produced,
	}, nil
}
