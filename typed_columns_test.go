package litelog

import (
	"context"
	"testing"
	"time"

	"zombiezen.com/go/sqlite"
)

func TestTypedColumns_Populated(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":test/int", ValueType: TypeInt, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":test/str", ValueType: TypeString, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":test/flt", ValueType: TypeFloat, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":test/bool", ValueType: TypeBool, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":test/ref", ValueType: TypeRef, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":test/inst", ValueType: TypeInst, Cardinality: CardOne,
	}))

	now := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	tx, err := db.Transact(ctx, []TxDatom{
		{E: TempID("e1"), A: ":test/int", V: int64(42)},
		{E: TempID("e1"), A: ":test/str", V: "hello"},
		{E: TempID("e1"), A: ":test/flt", V: float64(3.14)},
		{E: TempID("e1"), A: ":test/bool", V: true},
		{E: TempID("e1"), A: ":test/ref", V: int64(999)},
		{E: TempID("e1"), A: ":test/inst", V: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := tx.Tempids[TempID("e1")]

	// Verify typed columns are populated in current_datoms
	err = db.withConn(ctx, func(conn *sqlite.Conn) error {
		stmt, err := conn.Prepare("SELECT a, v_int, v_text, v_float, v_type FROM current_datoms WHERE e = ?")
		if err != nil {
			return err
		}
		defer stmt.Reset()
		stmt.BindInt64(1, eid)

		count := 0
		for {
			hasRow, err := stmt.Step()
			if err != nil {
				return err
			}
			if !hasRow {
				break
			}
			count++
			vtype := stmt.ColumnInt64(4)
			switch ValueType(vtype) {
			case TypeInt:
				if stmt.ColumnInt64(1) != 42 {
					t.Errorf("v_int: expected 42, got %d", stmt.ColumnInt64(1))
				}
			case TypeString:
				if stmt.ColumnText(2) != "hello" {
					t.Errorf("v_text: expected hello, got %s", stmt.ColumnText(2))
				}
			case TypeFloat:
				if stmt.ColumnFloat(3) != 3.14 {
					t.Errorf("v_float: expected 3.14, got %f", stmt.ColumnFloat(3))
				}
			case TypeBool:
				if stmt.ColumnInt64(1) != 1 {
					t.Errorf("v_int (bool): expected 1, got %d", stmt.ColumnInt64(1))
				}
			case TypeRef:
				if stmt.ColumnInt64(1) != 999 {
					t.Errorf("v_int (ref): expected 999, got %d", stmt.ColumnInt64(1))
				}
			case TypeInst:
				if stmt.ColumnInt64(1) != now.UnixMilli() {
					t.Errorf("v_int (inst): expected %d, got %d", now.UnixMilli(), stmt.ColumnInt64(1))
				}
			}
		}
		if count != 6 {
			t.Errorf("expected 6 datoms, got %d", count)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTypedColumns_QueryFilter(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":item/name", ValueType: TypeString, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":item/count", ValueType: TypeInt, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":item/price", ValueType: TypeFloat, Cardinality: CardOne,
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":item/name", V: "Widget"},
		{E: TempID("a"), A: ":item/count", V: int64(10)},
		{E: TempID("a"), A: ":item/price", V: float64(9.99)},
		{E: TempID("b"), A: ":item/name", V: "Gadget"},
		{E: TempID("b"), A: ":item/count", V: int64(5)},
		{E: TempID("b"), A: ":item/price", V: float64(19.99)},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Int filter via typed column
	qr, err := db.Query(ctx, `[:find ?name :where [?e :item/name ?name] [?e :item/count ?c] [(> ?c 7)]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 || qr.Rows[0][0] != "Widget" {
		t.Errorf("int filter: expected [Widget], got %v", qr.Rows)
	}

	// Float filter via typed column
	qr, err = db.Query(ctx, `[:find ?name :where [?e :item/name ?name] [?e :item/price ?p] [(> ?p 15.0)]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 || qr.Rows[0][0] != "Gadget" {
		t.Errorf("float filter: expected [Gadget], got %v", qr.Rows)
	}

	// String constant match via typed column
	qr, err = db.Query(ctx, `[:find ?c :where [?e :item/name "Widget"] [?e :item/count ?c]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 || qr.Rows[0][0] != int64(10) {
		t.Errorf("string match: expected [10], got %v", qr.Rows)
	}
}

func TestTypedColumns_Migration(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/migrate.db"

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":test/val", ValueType: TypeInt, Cardinality: CardOne,
	}))
	_, err = db.Transact(ctx, []TxDatom{
		{E: TempID("e"), A: ":test/val", V: int64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Reopen — migration runs again, should be idempotent
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	qr, err := db2.Query(ctx, `[:find ?v :where [?e :test/val ?v]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 || qr.Rows[0][0] != int64(1) {
		t.Errorf("expected [1] after reopen, got %v", qr.Rows)
	}
}
