package litelog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func openPartitioned(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "current.db")
	histPath := filepath.Join(dir, "history.db")
	db, err := Open(mainPath, Options{
		PoolSize:    2,
		HistoryPath: histPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestHotCold_Partitioned(t *testing.T) {
	db := openPartitioned(t)
	if !db.Partitioned() {
		t.Fatal("expected Partitioned() == true")
	}

	db2 := openMem(t)
	if db2.Partitioned() {
		t.Fatal("expected Partitioned() == false for in-memory DB")
	}
}

func TestHotCold_TransactAndQuery(t *testing.T) {
	db := openPartitioned(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":thing/name", ValueType: TypeString, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":thing/count", ValueType: TypeInt, Cardinality: CardOne,
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":thing/name", V: "alpha"},
		{E: TempID("a"), A: ":thing/count", V: int64(10)},
		{E: TempID("b"), A: ":thing/name", V: "beta"},
		{E: TempID("b"), A: ":thing/count", V: int64(20)},
	})
	if err != nil {
		t.Fatal(err)
	}

	qr, err := db.Query(ctx, `[:find ?name :where [?e :thing/name ?name]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(qr.Rows))
	}
}

func TestHotCold_AsOf(t *testing.T) {
	db := openPartitioned(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":item/name", ValueType: TypeString, Cardinality: CardOne,
	}))

	tx1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("x"), A: ":item/name", V: "original"},
	})
	if err != nil {
		t.Fatal(err)
	}

	eid := tx1.Tempids[TempID("x")]

	_, err = db.Transact(ctx, []TxDatom{
		{E: eid, A: ":item/name", V: "updated"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Current state should show "updated"
	qr, err := db.Query(ctx, `[:find ?name :where [?e :item/name ?name]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 || qr.Rows[0][0] != "updated" {
		t.Fatalf("expected updated, got %v", qr.Rows)
	}

	// AsOf tx1 should show "original"
	view := db.AsOf(tx1.TxID)
	qr2, err := view.Query(ctx, `[:find ?name :where [?e :item/name ?name]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr2.Rows) != 1 || qr2.Rows[0][0] != "original" {
		t.Fatalf("expected original from AsOf, got %v", qr2.Rows)
	}
}

func TestHotCold_Pull(t *testing.T) {
	db := openPartitioned(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":obj/name", ValueType: TypeString, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":obj/weight", ValueType: TypeFloat, Cardinality: CardOne,
	}))

	tx, err := db.Transact(ctx, []TxDatom{
		{E: TempID("o"), A: ":obj/name", V: "sword"},
		{E: TempID("o"), A: ":obj/weight", V: float64(3.5)},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := tx.Tempids[TempID("o")]

	pr, err := db.Pull(ctx, eid, PullWildcard())
	if err != nil {
		t.Fatal(err)
	}
	if pr[":obj/name"] != "sword" {
		t.Errorf("expected sword, got %v", pr[":obj/name"])
	}
	if pr[":obj/weight"] != float64(3.5) {
		t.Errorf("expected 3.5, got %v", pr[":obj/weight"])
	}
}

func TestHotCold_PullAsOf(t *testing.T) {
	db := openPartitioned(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":room/desc", ValueType: TypeString, Cardinality: CardOne,
	}))

	tx1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("r"), A: ":room/desc", V: "A dark cave"},
	})
	if err != nil {
		t.Fatal(err)
	}
	eid := tx1.Tempids[TempID("r")]

	_, err = db.Transact(ctx, []TxDatom{
		{E: eid, A: ":room/desc", V: "A bright meadow"},
	})
	if err != nil {
		t.Fatal(err)
	}

	view := db.AsOf(tx1.TxID)
	pr, err := view.Pull(ctx, eid, PullAttrs(":room/desc"))
	if err != nil {
		t.Fatal(err)
	}
	if pr[":room/desc"] != "A dark cave" {
		t.Errorf("expected 'A dark cave', got %v", pr[":room/desc"])
	}
}

func TestHotCold_SeparateFiles(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "current.db")
	histPath := filepath.Join(dir, "history.db")

	db, err := Open(mainPath, Options{PoolSize: 2, HistoryPath: histPath})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":x/val", ValueType: TypeInt, Cardinality: CardOne,
	}))
	_, err = db.Transact(ctx, []TxDatom{
		{E: TempID("e1"), A: ":x/val", V: int64(42)},
	})
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Verify both files exist on disk
	if _, err := os.Stat(mainPath); err != nil {
		t.Errorf("main DB file missing: %v", err)
	}
	if _, err := os.Stat(histPath); err != nil {
		t.Errorf("history DB file missing: %v", err)
	}

	// Reopen and verify data survives
	db2, err := Open(mainPath, Options{PoolSize: 2, HistoryPath: histPath})
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	qr, err := db2.Query(ctx, `[:find ?v :where [?e :x/val ?v]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 || qr.Rows[0][0] != int64(42) {
		t.Errorf("expected [42], got %v", qr.Rows)
	}
}

func TestHotCold_Filter(t *testing.T) {
	db := openPartitioned(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":score/val", ValueType: TypeInt, Cardinality: CardOne,
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":score/val", V: int64(100)},
		{E: TempID("b"), A: ":score/val", V: int64(50)},
		{E: TempID("c"), A: ":score/val", V: int64(200)},
	})
	if err != nil {
		t.Fatal(err)
	}

	qr, err := db.Query(ctx, `[:find ?v :where [?e :score/val ?v] [(> ?v 75)]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %v", len(qr.Rows), qr.Rows)
	}
}

func TestHotCold_UDF(t *testing.T) {
	db := openPartitioned(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":num/val", ValueType: TypeInt, Cardinality: CardOne,
	}))
	must(t, db.RegisterFunction("is-even", 1, true, func(args ...any) (any, error) {
		n, _ := args[0].(int64)
		return n%2 == 0, nil
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":num/val", V: int64(2)},
		{E: TempID("b"), A: ":num/val", V: int64(3)},
		{E: TempID("c"), A: ":num/val", V: int64(4)},
	})
	if err != nil {
		t.Fatal(err)
	}

	qr, err := db.Query(ctx, `[:find ?v :where [?e :num/val ?v] [(is-even ?v)]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 2 {
		t.Fatalf("expected 2 even numbers, got %d: %v", len(qr.Rows), qr.Rows)
	}
}

func TestHotCold_ValidTime(t *testing.T) {
	db := openPartitioned(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":event/name", ValueType: TypeString, Cardinality: CardOne,
	}))

	past := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.TransactWithValidTime(ctx, []TxDatom{
		{E: TempID("ev"), A: ":event/name", V: "past-event"},
	}, past)
	if err != nil {
		t.Fatal(err)
	}

	future := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = db.TransactWithValidTime(ctx, []TxDatom{
		{E: TempID("ev2"), A: ":event/name", V: "future-event"},
	}, future)
	if err != nil {
		t.Fatal(err)
	}

	// AsOfValidTime in 2025 should only see past-event
	view := db.AsOfValidTime(time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC))
	qr, err := view.Query(ctx, `[:find ?name :where [?e :event/name ?name]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 || qr.Rows[0][0] != "past-event" {
		t.Fatalf("expected [past-event], got %v", qr.Rows)
	}
}
