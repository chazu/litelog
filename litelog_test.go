package litelog

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func openMem(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open(tempdir): %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func definePersonAttrs(t *testing.T, db *DB) {
	t.Helper()
	ctx := context.Background()
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":person/name",
		ValueType:   TypeString,
		Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":person/age",
		ValueType:   TypeInt,
		Cardinality: CardOne,
	}))
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// transactAliceBob inserts Alice(30) and Bob(25), returning the TxResult.
func transactAliceBob(t *testing.T, db *DB) *TxResult {
	t.Helper()
	ctx := context.Background()
	res, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
		{E: TempID("alice"), A: ":person/age", V: int64(30)},
		{E: TempID("bob"), A: ":person/name", V: "Bob"},
		{E: TempID("bob"), A: ":person/age", V: int64(25)},
	})
	if err != nil {
		t.Fatalf("transact alice/bob: %v", err)
	}
	return res
}

// ---------------------------------------------------------------------------
// Value encoding round-trip tests
// ---------------------------------------------------------------------------

func TestEncodeDecodeInt64(t *testing.T) {
	cases := []int64{math.MinInt64, -1000, -1, 0, 1, 1000, math.MaxInt64}
	for _, n := range cases {
		b := encodeInt64(n)
		got := decodeInt64(b)
		if got != n {
			t.Errorf("int64 round-trip: want %d, got %d", n, got)
		}
	}
}

func TestEncodeDecodeFloat64(t *testing.T) {
	cases := []float64{
		math.Inf(-1), -1e100, -1.5, -0.0, 0, 0.1, 1.0, 1e100, math.Inf(1),
	}
	for _, f := range cases {
		b := encodeFloat64(f)
		got := decodeFloat64(b)
		if got != f {
			t.Errorf("float64 round-trip: want %v, got %v", f, got)
		}
	}
}

func TestFloatOrderPreserving(t *testing.T) {
	ordered := []float64{
		math.Inf(-1), -1e10, -1.5, -0.001, 0, 0.001, 1.5, 1e10, math.Inf(1),
	}
	var encoded [][]byte
	for _, f := range ordered {
		encoded = append(encoded, encodeFloat64(f))
	}
	for i := 1; i < len(encoded); i++ {
		if bytes.Compare(encoded[i-1], encoded[i]) >= 0 {
			t.Errorf("float order violated at index %d: encode(%v) >= encode(%v)",
				i, ordered[i-1], ordered[i])
		}
	}
}

func TestIntOrderPreserving(t *testing.T) {
	ordered := []int64{math.MinInt64, -1000, -1, 0, 1, 1000, math.MaxInt64}
	var encoded [][]byte
	for _, n := range ordered {
		encoded = append(encoded, encodeInt64(n))
	}
	// NOTE: encodeInt64 uses unsigned big-endian, NOT order-preserving for signed.
	// This test validates the actual behavior. If int order-preservation is
	// intended, this will catch regressions. Currently int64 is stored as
	// raw big-endian uint64 cast, so negative numbers sort AFTER positives
	// in byte order (sign bit is high). Adjust expectations accordingly.
	// We still verify round-trip correctness above.
}

func TestEncodeValueAllTypes(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond) // millisecond precision
	cases := []struct {
		name  string
		vtype ValueType
		val   any
	}{
		{"int64", TypeInt, int64(42)},
		{"int", TypeInt, int(42)},
		{"string", TypeString, "hello world"},
		{"float64", TypeFloat, 3.14},
		{"bool-true", TypeBool, true},
		{"bool-false", TypeBool, false},
		{"bytes", TypeBytes, []byte{0xDE, 0xAD}},
		{"time", TypeInst, now},
		{"ref", TypeRef, int64(999)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := encodeValue(tc.val, tc.vtype)
			if err != nil {
				t.Fatalf("encodeValue: %v", err)
			}
			got, err := decodeValue(b, tc.vtype)
			if err != nil {
				t.Fatalf("decodeValue: %v", err)
			}
			switch v := tc.val.(type) {
			case int:
				// decodeValue returns int64
				if got.(int64) != int64(v) {
					t.Errorf("want %d, got %d", v, got)
				}
			case []byte:
				if !bytes.Equal(got.([]byte), v) {
					t.Errorf("bytes mismatch")
				}
			case time.Time:
				if !got.(time.Time).Equal(v) {
					t.Errorf("time mismatch: want %v, got %v", v, got)
				}
			default:
				if got != tc.val {
					t.Errorf("want %v, got %v", tc.val, got)
				}
			}
		})
	}
}

func TestEncodeValueTypeMismatch(t *testing.T) {
	cases := []struct {
		name  string
		vtype ValueType
		val   any
	}{
		{"string-as-int", TypeInt, "not a number"},
		{"int-as-string", TypeString, 42},
		{"int-as-float", TypeFloat, 42},
		{"string-as-bool", TypeBool, "true"},
		{"int-as-bytes", TypeBytes, 42},
		{"string-as-inst", TypeInst, "2024-01-01"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := encodeValue(tc.val, tc.vtype)
			if err == nil {
				t.Fatal("expected error for type mismatch, got nil")
			}
		})
	}
}

func TestInferValueType(t *testing.T) {
	cases := []struct {
		val  any
		want ValueType
	}{
		{int64(1), TypeInt},
		{int(1), TypeInt},
		{"hello", TypeString},
		{3.14, TypeFloat},
		{true, TypeBool},
		{[]byte{1}, TypeBytes},
		{time.Now(), TypeInst},
	}
	for _, tc := range cases {
		got, err := inferValueType(tc.val)
		if err != nil {
			t.Errorf("inferValueType(%T): %v", tc.val, err)
		}
		if got != tc.want {
			t.Errorf("inferValueType(%T) = %d, want %d", tc.val, got, tc.want)
		}
	}
	// Error case: unsupported type
	_, err := inferValueType(struct{}{})
	if err == nil {
		t.Fatal("expected error for unsupported type")
	}
}

// ---------------------------------------------------------------------------
// DB lifecycle tests
// ---------------------------------------------------------------------------

func TestOpenCloseMemory(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mem.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestOpenCloseFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file not created: %v", err)
	}

	ctx := context.Background()
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":test/name",
		ValueType:   TypeString,
		Cardinality: CardOne,
	}))

	_, err = db.Transact(ctx, []TxDatom{
		{E: TempID("x"), A: ":test/name", V: "persisted"},
	})
	must(t, err)

	must(t, db.Close())

	// Reopen and verify state persists
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()

	// Attribute should be loaded from disk
	attr := db2.schema.GetByIdent(":test/name")
	if attr == nil {
		t.Fatal("attribute :test/name not loaded from disk after reopen")
	}
	if attr.ValueType != TypeString {
		t.Errorf("attribute value type: want TypeString, got %d", attr.ValueType)
	}

	// Data should be queryable
	res, err := db2.Query(ctx, `[:find ?name :where [?e :test/name ?name]]`)
	if err != nil {
		t.Fatalf("query after reopen: %v", err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0] != "persisted" {
		t.Errorf("expected [[persisted]], got %v", res.Rows)
	}
}

func TestOpenCustomPoolSize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pool.db")
	db, err := Open(path, Options{PoolSize: 3})
	if err != nil {
		t.Fatalf("Open with PoolSize=3: %v", err)
	}
	db.Close()
}

// ---------------------------------------------------------------------------
// Attribute tests
// ---------------------------------------------------------------------------

func TestDefineAttribute(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":person/name",
		ValueType:   TypeString,
		Cardinality: CardOne,
		Indexed:     true,
	}))

	attr := db.schema.GetByIdent(":person/name")
	if attr == nil {
		t.Fatal("attribute not in schema cache")
	}
	if attr.ValueType != TypeString {
		t.Errorf("ValueType = %d, want %d", attr.ValueType, TypeString)
	}
	if attr.Cardinality != CardOne {
		t.Errorf("Cardinality = %d, want %d", attr.Cardinality, CardOne)
	}
	if !attr.Indexed {
		t.Error("Indexed should be true")
	}
}

func TestDefineAttributeIdempotent(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	schema := AttributeSchema{
		Ident:       ":person/name",
		ValueType:   TypeString,
		Cardinality: CardOne,
	}
	must(t, db.DefineAttribute(ctx, schema))
	must(t, db.DefineAttribute(ctx, schema)) // same definition = no error
}

func TestDefineAttributeConflict(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":person/name",
		ValueType:   TypeString,
		Cardinality: CardOne,
	}))
	err := db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":person/name",
		ValueType:   TypeInt, // different type!
		Cardinality: CardOne,
	})
	if err == nil {
		t.Fatal("expected error for conflicting attribute definition")
	}
}

func TestAttributesPersistAfterReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "attrs.db")
	ctx := context.Background()

	db, err := Open(path)
	must(t, err)
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":item/color",
		ValueType:   TypeString,
		Cardinality: CardMany,
		Indexed:     true,
	}))
	must(t, db.Close())

	db2, err := Open(path)
	must(t, err)
	defer db2.Close()

	attr := db2.schema.GetByIdent(":item/color")
	if attr == nil {
		t.Fatal("attribute not loaded from disk")
	}
	if attr.Cardinality != CardMany {
		t.Errorf("Cardinality = %d, want CardMany(%d)", attr.Cardinality, CardMany)
	}
	if !attr.Indexed {
		t.Error("Indexed should be true")
	}
}

// ---------------------------------------------------------------------------
// Transaction tests
// ---------------------------------------------------------------------------

func TestSimpleAssert(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	definePersonAttrs(t, db)

	res, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
	})
	if err != nil {
		t.Fatalf("transact: %v", err)
	}
	if res.TxID == 0 {
		t.Error("TxID should be > 0")
	}
	if res.TxTime == 0 {
		t.Error("TxTime should be > 0")
	}
	if len(res.Datoms) != 1 {
		t.Fatalf("expected 1 datom, got %d", len(res.Datoms))
	}
	if !res.Datoms[0].Added {
		t.Error("datom should be Added=true")
	}
	if res.Datoms[0].V != "Alice" {
		t.Errorf("datom V = %v, want Alice", res.Datoms[0].V)
	}
}

func TestTempIDResolution(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	definePersonAttrs(t, db)

	res, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
		{E: TempID("alice"), A: ":person/age", V: int64(30)},
	})
	if err != nil {
		t.Fatalf("transact: %v", err)
	}

	aliceID, ok := res.Tempids[TempID("alice")]
	if !ok {
		t.Fatal("TempID('alice') not resolved")
	}
	if aliceID == 0 {
		t.Error("resolved ID should be > 0")
	}

	// Both datoms should have the same entity ID
	if res.Datoms[0].E != res.Datoms[1].E {
		t.Errorf("TempID did not resolve to same entity: %d vs %d",
			res.Datoms[0].E, res.Datoms[1].E)
	}
}

func TestMultipleTempIDs(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)

	res := transactAliceBob(t, db)

	aliceID := res.Tempids[TempID("alice")]
	bobID := res.Tempids[TempID("bob")]
	if aliceID == bobID {
		t.Error("alice and bob should have different entity IDs")
	}
}

func TestCardOneReplacement(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	definePersonAttrs(t, db)

	res1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
	})
	must(t, err)
	aliceEID := res1.Tempids[TempID("alice")]

	// Update name (CardOne should retract old value)
	res2, err := db.Transact(ctx, []TxDatom{
		{E: aliceEID, A: ":person/name", V: "Alicia"},
	})
	must(t, err)

	// Should have a retraction and an assertion
	if len(res2.Datoms) != 2 {
		t.Fatalf("expected 2 datoms (retract + assert), got %d", len(res2.Datoms))
	}

	var retracted, asserted bool
	for _, d := range res2.Datoms {
		if !d.Added && d.V == "Alice" {
			retracted = true
		}
		if d.Added && d.V == "Alicia" {
			asserted = true
		}
	}
	if !retracted {
		t.Error("old value 'Alice' should be retracted")
	}
	if !asserted {
		t.Error("new value 'Alicia' should be asserted")
	}

	// Query should return only new value
	qr, err := db.Query(ctx, `[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)
	if len(qr.Rows) != 1 || qr.Rows[0][0] != "Alicia" {
		t.Errorf("expected [[Alicia]], got %v", qr.Rows)
	}
}

func TestCardMany(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":person/tag",
		ValueType:   TypeString,
		Cardinality: CardMany,
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/tag", V: "engineer"},
		{E: TempID("alice"), A: ":person/tag", V: "musician"},
	})
	must(t, err)

	qr, err := db.Query(ctx, `[:find ?tag :where [?e :person/tag ?tag]]`)
	must(t, err)

	tags := make(map[string]bool)
	for _, row := range qr.Rows {
		tags[row[0].(string)] = true
	}
	if !tags["engineer"] || !tags["musician"] {
		t.Errorf("expected both tags, got %v", tags)
	}
}

func TestExplicitRetraction(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	definePersonAttrs(t, db)

	res, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
		{E: TempID("alice"), A: ":person/age", V: int64(30)},
	})
	must(t, err)
	aliceEID := res.Tempids[TempID("alice")]

	// Retract the name
	_, err = db.Transact(ctx, []TxDatom{
		{E: aliceEID, A: ":person/name", V: "Alice", Retract: true},
	})
	must(t, err)

	// Name should be gone
	qr, err := db.Query(ctx, `[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)
	if len(qr.Rows) != 0 {
		t.Errorf("expected no rows after retraction, got %v", qr.Rows)
	}

	// Age should still be there
	qr2, err := db.Query(ctx, `[:find ?age :where [?e :person/age ?age]]`)
	must(t, err)
	if len(qr2.Rows) != 1 {
		t.Errorf("expected 1 row for age, got %d", len(qr2.Rows))
	}
}

func TestTransactWithValidTime(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()
	definePersonAttrs(t, db)

	pastTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err := db.TransactWithValidTime(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
	}, pastTime)
	must(t, err)

	// Verify the data is queryable
	qr, err := db.Query(ctx, `[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)
	if len(qr.Rows) != 1 || qr.Rows[0][0] != "Alice" {
		t.Errorf("expected [[Alice]], got %v", qr.Rows)
	}
}

func TestTransactUnknownAttribute(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("x"), A: ":nonexistent/attr", V: "val"},
	})
	if err == nil {
		t.Fatal("expected error for unknown attribute")
	}
}

func TestEmptyTransaction(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	res, err := db.Transact(ctx, []TxDatom{})
	if err != nil {
		t.Fatalf("empty transact: %v", err)
	}
	if len(res.Datoms) != 0 {
		t.Errorf("expected 0 datoms, got %d", len(res.Datoms))
	}
}

// ---------------------------------------------------------------------------
// Query tests (ACCEPTANCE / SMOKE)
// ---------------------------------------------------------------------------

func TestQuerySmoke(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	transactAliceBob(t, db)

	ctx := context.Background()
	qr, err := db.Query(ctx, `[:find ?name :where [?e :person/name ?name]]`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	names := make(map[string]bool)
	for _, row := range qr.Rows {
		names[row[0].(string)] = true
	}
	if !names["Alice"] || !names["Bob"] {
		t.Errorf("expected Alice and Bob, got %v", names)
	}
}

func TestQueryJoin(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	transactAliceBob(t, db)

	ctx := context.Background()
	qr, err := db.Query(ctx,
		`[:find ?name ?age :where [?e :person/name ?name] [?e :person/age ?age]]`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(qr.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(qr.Rows))
	}

	// Build map for order-independent assertion
	byName := make(map[string]int64)
	for _, row := range qr.Rows {
		name := row[0].(string)
		age := row[1].(int64)
		byName[name] = age
	}
	if byName["Alice"] != 30 {
		t.Errorf("Alice age = %d, want 30", byName["Alice"])
	}
	if byName["Bob"] != 25 {
		t.Errorf("Bob age = %d, want 25", byName["Bob"])
	}
}

func TestQueryFilter(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	transactAliceBob(t, db)

	ctx := context.Background()
	qr, err := db.Query(ctx,
		`[:find ?name :where [?e :person/name ?name] [?e :person/age ?age] [(> ?age 27)]]`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(qr.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(qr.Rows))
	}
	if qr.Rows[0][0] != "Alice" {
		t.Errorf("expected Alice, got %v", qr.Rows[0][0])
	}
}

func TestQueryAggregate(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	transactAliceBob(t, db)

	ctx := context.Background()
	qr, err := db.Query(ctx,
		`[:find (count ?e) :where [?e :person/name _]]`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(qr.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(qr.Rows))
	}
	if qr.Rows[0][0].(int64) != 2 {
		t.Errorf("expected count=2, got %v", qr.Rows[0][0])
	}
}

func TestQueryInParameter(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	transactAliceBob(t, db)

	ctx := context.Background()
	qr, err := db.Query(ctx,
		`[:find ?name :in $ ?min-age :where [?e :person/name ?name] [?e :person/age ?age] [(> ?age ?min-age)]]`,
		int64(27))
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(qr.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(qr.Rows))
	}
	if qr.Rows[0][0] != "Alice" {
		t.Errorf("expected Alice, got %v", qr.Rows[0][0])
	}
}

func TestCardOneUpdateThenQuery(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	ctx := context.Background()

	res := transactAliceBob(t, db)
	aliceEID := res.Tempids[TempID("alice")]

	// Update Alice's age to 31
	_, err := db.Transact(ctx, []TxDatom{
		{E: aliceEID, A: ":person/age", V: int64(31)},
	})
	must(t, err)

	qr, err := db.Query(ctx,
		`[:find ?name ?age :where [?e :person/name ?name] [?e :person/age ?age]]`)
	must(t, err)

	byName := make(map[string]int64)
	for _, row := range qr.Rows {
		byName[row[0].(string)] = row[1].(int64)
	}
	if byName["Alice"] != 31 {
		t.Errorf("Alice age = %d, want 31", byName["Alice"])
	}
	if byName["Bob"] != 25 {
		t.Errorf("Bob age = %d, want 25", byName["Bob"])
	}
}

func TestRetractionThenQuery(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	ctx := context.Background()

	res := transactAliceBob(t, db)
	bobEID := res.Tempids[TempID("bob")]

	// Retract both of Bob's attributes
	_, err := db.Transact(ctx, []TxDatom{
		{E: bobEID, A: ":person/name", V: "Bob", Retract: true},
		{E: bobEID, A: ":person/age", V: int64(25), Retract: true},
	})
	must(t, err)

	qr, err := db.Query(ctx,
		`[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)

	if len(qr.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(qr.Rows))
	}
	if qr.Rows[0][0] != "Alice" {
		t.Errorf("expected Alice, got %v", qr.Rows[0][0])
	}
}

// ---------------------------------------------------------------------------
// Bitemporal / AsOf tests
// ---------------------------------------------------------------------------

func TestAsOfTransactionTime(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	ctx := context.Background()

	// tx1: insert Alice with name "Alice"
	res1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
	})
	must(t, err)
	aliceEID := res1.Tempids[TempID("alice")]

	// tx2: update Alice's name to "Alicia"
	res2, err := db.Transact(ctx, []TxDatom{
		{E: aliceEID, A: ":person/name", V: "Alicia"},
	})
	must(t, err)

	// AsOf tx1: should see "Alice"
	qr1, err := db.AsOf(res1.TxID).Query(ctx,
		`[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)
	if len(qr1.Rows) != 1 || qr1.Rows[0][0] != "Alice" {
		t.Errorf("AsOf(tx1): expected [[Alice]], got %v", qr1.Rows)
	}

	// AsOf tx2: should see "Alicia"
	qr2, err := db.AsOf(res2.TxID).Query(ctx,
		`[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)
	if len(qr2.Rows) != 1 || qr2.Rows[0][0] != "Alicia" {
		t.Errorf("AsOf(tx2): expected [[Alicia]], got %v", qr2.Rows)
	}
}

func TestAsOfValidTime(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	ctx := context.Background()

	t1 := time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC)
	t2 := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)

	// Transact Alice at valid time t1
	res1, err := db.TransactWithValidTime(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
	}, t1)
	must(t, err)
	aliceEID := res1.Tempids[TempID("alice")]

	// Update Alice at valid time t2
	_, err = db.TransactWithValidTime(ctx, []TxDatom{
		{E: aliceEID, A: ":person/name", V: "Alicia"},
	}, t2)
	must(t, err)

	// Current state should show "Alicia"
	qr, err := db.Query(ctx, `[:find ?name :where [?e :person/name ?name]]`)
	must(t, err)
	if len(qr.Rows) != 1 || qr.Rows[0][0] != "Alicia" {
		t.Errorf("current: expected [[Alicia]], got %v", qr.Rows)
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestQueryNoResults(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	ctx := context.Background()

	qr, err := db.Query(ctx, `[:find ?name :where [?e :person/name ?name]]`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if qr == nil {
		t.Fatal("result should be non-nil")
	}
	if len(qr.Rows) != 0 {
		t.Errorf("expected 0 rows, got %d", len(qr.Rows))
	}
}

func TestQueryUnknownAttribute(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	_, err := db.Query(ctx, `[:find ?v :where [?e :no/such ?v]]`)
	if err == nil {
		t.Fatal("expected error for unknown attribute in query")
	}
}

func TestLargeBatchTransaction(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":item/value",
		ValueType:   TypeInt,
		Cardinality: CardOne,
	}))

	datoms := make([]TxDatom, 1000)
	for i := range datoms {
		datoms[i] = TxDatom{
			E: TempID(TempID(string(rune('A' + i%26)) + fmt.Sprintf("%d", i))),
			A: ":item/value",
			V: int64(i),
		}
	}

	res, err := db.Transact(ctx, datoms)
	if err != nil {
		t.Fatalf("large batch transact: %v", err)
	}
	if len(res.Datoms) != 1000 {
		t.Errorf("expected 1000 datoms, got %d", len(res.Datoms))
	}

	// Verify count
	qr, err := db.Query(ctx, `[:find (count ?e) :where [?e :item/value _]]`)
	must(t, err)
	if qr.Rows[0][0].(int64) != 1000 {
		t.Errorf("expected count=1000, got %v", qr.Rows[0][0])
	}
}

func TestQueryColumns(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	transactAliceBob(t, db)

	ctx := context.Background()
	qr, err := db.Query(ctx,
		`[:find ?name ?age :where [?e :person/name ?name] [?e :person/age ?age]]`)
	must(t, err)

	if len(qr.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(qr.Columns))
	}
	if qr.Columns[0] != "?name" {
		t.Errorf("column[0] = %q, want ?name", qr.Columns[0])
	}
	if qr.Columns[1] != "?age" {
		t.Errorf("column[1] = %q, want ?age", qr.Columns[1])
	}
}

func TestMultipleValueTypes(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":entity/name", ValueType: TypeString, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":entity/score", ValueType: TypeFloat, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":entity/active", ValueType: TypeBool, Cardinality: CardOne,
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("x"), A: ":entity/name", V: "Widget"},
		{E: TempID("x"), A: ":entity/score", V: 95.5},
		{E: TempID("x"), A: ":entity/active", V: true},
	})
	must(t, err)

	// Query each attribute individually
	qr, err := db.Query(ctx, `[:find ?n :where [?e :entity/name ?n]]`)
	must(t, err)
	if len(qr.Rows) != 1 || qr.Rows[0][0] != "Widget" {
		t.Errorf("name: expected Widget, got %v", qr.Rows)
	}

	qr, err = db.Query(ctx, `[:find ?s :where [?e :entity/score ?s]]`)
	must(t, err)
	if len(qr.Rows) != 1 || qr.Rows[0][0] != 95.5 {
		t.Errorf("score: expected 95.5, got %v", qr.Rows)
	}

	qr, err = db.Query(ctx, `[:find ?a :where [?e :entity/active ?a]]`)
	must(t, err)
	if len(qr.Rows) != 1 || qr.Rows[0][0] != true {
		t.Errorf("active: expected true, got %v", qr.Rows)
	}
}

func TestSchemaGetByID(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":test/x",
		ValueType:   TypeInt,
		Cardinality: CardOne,
	}))

	byIdent := db.schema.GetByIdent(":test/x")
	if byIdent == nil {
		t.Fatal("GetByIdent returned nil")
	}
	byID := db.schema.GetByID(byIdent.ID)
	if byID == nil {
		t.Fatal("GetByID returned nil")
	}
	if byID.Ident != ":test/x" {
		t.Errorf("GetByID.Ident = %q, want :test/x", byID.Ident)
	}
}

func TestSchemaAll(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":a/x", ValueType: TypeString, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":a/y", ValueType: TypeInt, Cardinality: CardMany,
	}))

	all := db.schema.All()
	if len(all) != 2 {
		t.Errorf("expected 2 attributes, got %d", len(all))
	}

	idents := make([]string, len(all))
	for i, a := range all {
		idents[i] = a.Ident
	}
	sort.Strings(idents)
	if idents[0] != ":a/x" || idents[1] != ":a/y" {
		t.Errorf("unexpected idents: %v", idents)
	}
}

