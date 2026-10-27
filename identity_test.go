package litelog

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zombiezen.com/go/sqlite"
	"zombiezen.com/go/sqlite/sqlitex"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func defineUserAttrs(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":user/email",
		ValueType:   TypeString,
		Cardinality: CardOne,
		Indexed:     true,
		Identity:    true,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":user/name",
		ValueType:   TypeString,
		Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":user/nick",
		ValueType:   TypeString,
		Cardinality: CardOne,
		Unique:      true,
	}))
}

func userNames(t *testing.T, db *DB) [][]any {
	t.Helper()
	qr, err := db.Query(context.Background(),
		`[:find ?e ?name :where [?e :user/name ?name]]`)
	must(t, err)
	return qr.Rows
}

// ---------------------------------------------------------------------------
// TransactOpts tests
// ---------------------------------------------------------------------------

func TestTransactWithOptsTxTime(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	ctx := context.Background()

	recorded := time.Date(2024, 3, 1, 12, 0, 0, 0, time.UTC)
	res, err := db.TransactWithOpts(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
	}, TransactOpts{TxTime: recorded})
	must(t, err)

	if res.TxTime != recorded.UnixMilli() {
		t.Errorf("TxTime = %d, want %d", res.TxTime, recorded.UnixMilli())
	}

	var stored int64
	must(t, db.withConn(ctx, func(conn *sqlite.Conn) error {
		return sqlitex.Execute(conn, "SELECT tx_time FROM transactions WHERE id = ?",
			&sqlitex.ExecOptions{
				Args: []any{res.TxID},
				ResultFunc: func(stmt *sqlite.Stmt) error {
					stored = stmt.ColumnInt64(0)
					return nil
				},
			})
	}))
	if stored != recorded.UnixMilli() {
		t.Errorf("stored tx_time = %d, want %d", stored, recorded.UnixMilli())
	}

	// valid_from defaults to TxTime: visible after, not before.
	qr, err := db.AsOfValidTime(recorded.Add(time.Hour)).Query(ctx,
		`[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)
	if len(qr.Rows) != 1 {
		t.Errorf("valid after TxTime: expected 1 row, got %v", qr.Rows)
	}
	qr, err = db.AsOfValidTime(recorded.Add(-time.Hour)).Query(ctx,
		`[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)
	if len(qr.Rows) != 0 {
		t.Errorf("valid before TxTime: expected no rows, got %v", qr.Rows)
	}
}

func TestTransactWithOptsValidTime(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	ctx := context.Background()

	recorded := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	valid := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	res, err := db.TransactWithOpts(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
	}, TransactOpts{TxTime: recorded, ValidTime: valid})
	must(t, err)
	if res.TxTime != recorded.UnixMilli() {
		t.Errorf("TxTime = %d, want %d", res.TxTime, recorded.UnixMilli())
	}

	qr, err := db.AsOfValidTime(valid.Add(time.Hour)).Query(ctx,
		`[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)
	if len(qr.Rows) != 1 {
		t.Errorf("expected Alice valid in 2020, got %v", qr.Rows)
	}
}

// ---------------------------------------------------------------------------
// Identity upsert tests
// ---------------------------------------------------------------------------

func TestIdentityUpsert(t *testing.T) {
	db := openMem(t)
	defineUserAttrs(t, db)
	ctx := context.Background()

	res1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":user/email", V: "a@example.com"},
		{E: TempID("a"), A: ":user/name", V: "Alice"},
	})
	must(t, err)
	alice := res1.Tempids[TempID("a")]

	res2, err := db.Transact(ctx, []TxDatom{
		{E: TempID("b"), A: ":user/email", V: "a@example.com"},
		{E: TempID("b"), A: ":user/name", V: "Alicia"},
	})
	must(t, err)
	if got := res2.Tempids[TempID("b")]; got != alice {
		t.Errorf("tempid b resolved to %d, want existing entity %d", got, alice)
	}

	rows := userNames(t, db)
	if len(rows) != 1 || rows[0][0] != alice || rows[0][1] != "Alicia" {
		t.Errorf("expected [[%d Alicia]], got %v", alice, rows)
	}
}

func TestIdentityUpsertDatomOrder(t *testing.T) {
	db := openMem(t)
	defineUserAttrs(t, db)
	ctx := context.Background()

	res1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":user/email", V: "a@example.com"},
	})
	must(t, err)
	alice := res1.Tempids[TempID("a")]

	// Non-identity datom precedes the identity datom for the same TempID.
	_, err = db.Transact(ctx, []TxDatom{
		{E: TempID("b"), A: ":user/name", V: "Alice"},
		{E: TempID("b"), A: ":user/email", V: "a@example.com"},
	})
	must(t, err)

	rows := userNames(t, db)
	if len(rows) != 1 || rows[0][0] != alice {
		t.Errorf("expected name on entity %d, got %v", alice, rows)
	}
}

func TestIdentitySharedNewValueInOneTx(t *testing.T) {
	db := openMem(t)
	defineUserAttrs(t, db)
	ctx := context.Background()

	res, err := db.Transact(ctx, []TxDatom{
		{E: TempID("x"), A: ":user/email", V: "x@example.com"},
		{E: TempID("y"), A: ":user/email", V: "x@example.com"},
		{E: TempID("y"), A: ":user/name", V: "Xavier"},
	})
	must(t, err)
	if res.Tempids[TempID("x")] != res.Tempids[TempID("y")] {
		t.Errorf("tempids x and y should share an entity, got %v", res.Tempids)
	}
}

func TestIdentityConflictExplicitEntity(t *testing.T) {
	db := openMem(t)
	defineUserAttrs(t, db)
	ctx := context.Background()

	res, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":user/email", V: "a@example.com"},
		{E: TempID("b"), A: ":user/email", V: "b@example.com"},
	})
	must(t, err)
	bob := res.Tempids[TempID("b")]

	_, err = db.Transact(ctx, []TxDatom{
		{E: bob, A: ":user/email", V: "a@example.com"},
	})
	if err == nil || !strings.Contains(err.Error(), "already held by entity") {
		t.Fatalf("expected identity conflict, got %v", err)
	}

	// Failed transaction left no trace.
	qr, err := db.Query(ctx, `[:find ?email :where [?e :user/email ?email]]`)
	must(t, err)
	if len(qr.Rows) != 2 {
		t.Errorf("expected 2 emails after rollback, got %v", qr.Rows)
	}
}

func TestIdentityTempIDResolvesToTwoEntities(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	defineUserAttrs(t, db)
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":user/handle",
		ValueType:   TypeString,
		Cardinality: CardOne,
		Identity:    true,
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":user/email", V: "a@example.com"},
		{E: TempID("b"), A: ":user/handle", V: "bob"},
	})
	must(t, err)

	_, err = db.Transact(ctx, []TxDatom{
		{E: TempID("c"), A: ":user/email", V: "a@example.com"},
		{E: TempID("c"), A: ":user/handle", V: "bob"},
	})
	if err == nil || !strings.Contains(err.Error(), "resolves to both") {
		t.Fatalf("expected tempid conflict, got %v", err)
	}
}

func TestUniqueWithoutIdentityDoesNotUpsert(t *testing.T) {
	db := openMem(t)
	defineUserAttrs(t, db)
	ctx := context.Background()

	res1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":user/nick", V: "al"},
	})
	must(t, err)
	res2, err := db.Transact(ctx, []TxDatom{
		{E: TempID("b"), A: ":user/nick", V: "al"},
	})
	must(t, err)
	if res1.Tempids[TempID("a")] == res2.Tempids[TempID("b")] {
		t.Error("Unique (non-Identity) attribute should not upsert")
	}
}

// ---------------------------------------------------------------------------
// LookupRef tests
// ---------------------------------------------------------------------------

func TestLookupRef(t *testing.T) {
	db := openMem(t)
	defineUserAttrs(t, db)
	ctx := context.Background()

	res, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":user/email", V: "a@example.com"},
		{E: TempID("a"), A: ":user/name", V: "Alice"},
	})
	must(t, err)
	alice := res.Tempids[TempID("a")]

	ref := LookupRef{A: ":user/email", V: "a@example.com"}
	_, err = db.Transact(ctx, []TxDatom{
		{E: ref, A: ":user/name", V: "Alice", Retract: true},
	})
	must(t, err)
	if rows := userNames(t, db); len(rows) != 0 {
		t.Errorf("expected name retracted, got %v", rows)
	}

	_, err = db.Transact(ctx, []TxDatom{
		{E: ref, A: ":user/name", V: "Alicia"},
	})
	must(t, err)
	rows := userNames(t, db)
	if len(rows) != 1 || rows[0][0] != alice {
		t.Errorf("expected name on entity %d, got %v", alice, rows)
	}
}

func TestLookupRefErrors(t *testing.T) {
	db := openMem(t)
	defineUserAttrs(t, db)
	ctx := context.Background()

	cases := []struct {
		name string
		ref  LookupRef
		want string
	}{
		{"missing entity", LookupRef{A: ":user/email", V: "nobody@example.com"}, "no such entity"},
		{"not identity", LookupRef{A: ":user/nick", V: "al"}, "not an identity attribute"},
		{"unknown attribute", LookupRef{A: ":user/nope", V: "x"}, "unknown attribute"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.Transact(ctx, []TxDatom{
				{E: tc.ref, A: ":user/name", V: "X"},
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Schema persistence and migration
// ---------------------------------------------------------------------------

func TestIdentityPersistsAfterReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "identity.db")
	ctx := context.Background()

	db, err := Open(path)
	must(t, err)
	defineUserAttrs(t, db)
	must(t, db.Close())

	db2, err := Open(path)
	must(t, err)
	defer db2.Close()

	attr := db2.schema.GetByIdent(":user/email")
	if attr == nil || !attr.Identity {
		t.Fatalf("Identity not loaded from disk: %+v", attr)
	}
	// Idempotent redefinition still matches.
	must(t, db2.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":user/email",
		ValueType:   TypeString,
		Cardinality: CardOne,
		Indexed:     true,
		Identity:    true,
	}))
	// Changing Identity is a conflicting definition.
	err = db2.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":user/email",
		ValueType:   TypeString,
		Cardinality: CardOne,
		Indexed:     true,
	})
	if err == nil {
		t.Error("expected conflict when Identity differs")
	}
}

func TestMigrateAddsUniqueIdentityColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	ctx := context.Background()

	db, err := Open(path)
	must(t, err)
	definePersonAttrs(t, db)
	// Simulate a database created before unique_identity existed.
	must(t, db.withConn(ctx, func(conn *sqlite.Conn) error {
		return sqlitex.ExecuteTransient(conn, "ALTER TABLE attributes DROP COLUMN unique_identity", nil)
	}))
	must(t, db.Close())

	db2, err := Open(path)
	must(t, err)
	defer db2.Close()
	if db2.schema.GetByIdent(":person/name") == nil {
		t.Fatal("attributes not loaded after migration")
	}
	defineUserAttrs(t, db2)
	if !db2.schema.GetByIdent(":user/email").Identity {
		t.Error("Identity not set on attribute defined after migration")
	}
}

func TestAsOfTxTime(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	ctx := context.Background()
	t1 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(24 * time.Hour)
	res, err := db.TransactWithOpts(ctx, []TxDatom{{E: TempID("a"), A: ":person/name", V: "Alice"}}, TransactOpts{TxTime: t1})
	must(t, err)
	alice := res.Tempids[TempID("a")]
	_, err = db.TransactWithOpts(ctx, []TxDatom{{E: alice, A: ":person/name", V: "Alicia"}}, TransactOpts{TxTime: t2})
	must(t, err)

	if _, ok, err := db.AsOfTxTime(ctx, t1.Add(-time.Hour)); err != nil || ok {
		t.Errorf("before any tx: ok=%v err=%v", ok, err)
	}
	for at, want := range map[time.Time]string{t1: "Alice", t1.Add(time.Hour): "Alice", t2: "Alicia", t2.Add(time.Hour): "Alicia"} {
		view, ok, err := db.AsOfTxTime(ctx, at)
		must(t, err)
		if !ok {
			t.Fatalf("no view at %v", at)
		}
		r, err := view.Pull(ctx, alice, PullAttrs(":person/name"))
		must(t, err)
		if r[":person/name"] != want {
			t.Errorf("at %v: name %v, want %s", at, r[":person/name"], want)
		}
	}
}
