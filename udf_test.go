package litelog

import (
	"context"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestUDF_BooleanPredicate(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":person/name", ValueType: TypeString, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":person/age", ValueType: TypeInt, Cardinality: CardOne,
	}))

	must(t, db.RegisterFunction("is-adult", 1, true, func(args ...any) (any, error) {
		age, ok := args[0].(int64)
		if !ok {
			return false, nil
		}
		return age >= 18, nil
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
		{E: TempID("alice"), A: ":person/age", V: int64(30)},
		{E: TempID("bob"), A: ":person/name", V: "Bob"},
		{E: TempID("bob"), A: ":person/age", V: int64(12)},
	})
	if err != nil {
		t.Fatal(err)
	}

	qr, err := db.Query(ctx, `[:find ?name :where [?e :person/name ?name] [?e :person/age ?age] [(is-adult ?age)]]`)
	if err != nil {
		t.Fatal(err)
	}

	if len(qr.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(qr.Rows))
	}
	if qr.Rows[0][0] != "Alice" {
		t.Errorf("expected Alice, got %v", qr.Rows[0][0])
	}
}

func TestUDF_StringFunction(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":item/desc", ValueType: TypeString, Cardinality: CardOne,
	}))

	must(t, db.RegisterFunction("contains", 2, true, func(args ...any) (any, error) {
		haystack, ok1 := args[0].(string)
		needle, ok2 := args[1].(string)
		if !ok1 || !ok2 {
			return false, nil
		}
		return strings.Contains(haystack, needle), nil
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":item/desc", V: "The quick brown fox"},
		{E: TempID("b"), A: ":item/desc", V: "A lazy dog"},
	})
	if err != nil {
		t.Fatal(err)
	}

	qr, err := db.Query(ctx, `[:find ?desc :where [?e :item/desc ?desc] [(contains ?desc "fox")]]`)
	if err != nil {
		t.Fatal(err)
	}

	if len(qr.Rows) != 1 || qr.Rows[0][0] != "The quick brown fox" {
		t.Errorf("expected fox row, got %v", qr.Rows)
	}
}

func TestUDF_MultiArg(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":point/x", ValueType: TypeFloat, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":point/y", ValueType: TypeFloat, Cardinality: CardOne,
	}))

	// Boolean predicate: returns true if point is near origin
	must(t, db.RegisterFunction("near-origin", 2, true, func(args ...any) (any, error) {
		x, ok1 := args[0].(float64)
		y, ok2 := args[1].(float64)
		if !ok1 || !ok2 {
			return false, fmt.Errorf("expected float args, got %T, %T", args[0], args[1])
		}
		dist := math.Sqrt(x*x + y*y)
		return dist < 5.0, nil
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("near"), A: ":point/x", V: float64(1.0)},
		{E: TempID("near"), A: ":point/y", V: float64(1.0)},
		{E: TempID("far"), A: ":point/x", V: float64(10.0)},
		{E: TempID("far"), A: ":point/y", V: float64(10.0)},
	})
	if err != nil {
		t.Fatal(err)
	}

	qr, err := db.Query(ctx, `[:find ?x ?y :where [?e :point/x ?x] [?e :point/y ?y] [(near-origin ?x ?y)]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 {
		t.Fatalf("expected 1 near point, got %d: %v", len(qr.Rows), qr.Rows)
	}
	if qr.Rows[0][0] != float64(1.0) {
		t.Errorf("expected x=1.0, got %v", qr.Rows[0][0])
	}
}

func TestUDF_Deterministic(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	callCount := 0
	must(t, db.RegisterFunction("counter", 0, false, func(args ...any) (any, error) {
		callCount++
		return int64(callCount), nil
	}))

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":test/val", ValueType: TypeInt, Cardinality: CardOne,
	}))
	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("e"), A: ":test/val", V: int64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Non-deterministic function should be callable
	// Just verify it doesn't error
	_, err = db.Query(ctx, `[:find ?v :where [?e :test/val ?v] [(counter)]]`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestUDF_RegisterMultiple(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":test/val", ValueType: TypeInt, Cardinality: CardOne,
	}))

	// Register first UDF
	must(t, db.RegisterFunction("is-positive", 1, true, func(args ...any) (any, error) {
		n, _ := args[0].(int64)
		return n > 0, nil
	}))

	// Register second UDF
	must(t, db.RegisterFunction("is-even", 1, true, func(args ...any) (any, error) {
		n, _ := args[0].(int64)
		return n%2 == 0, nil
	}))

	_, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":test/val", V: int64(4)},
		{E: TempID("b"), A: ":test/val", V: int64(3)},
		{E: TempID("c"), A: ":test/val", V: int64(-2)},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Both UDFs as predicates: positive AND even → only 4
	qr, err := db.Query(ctx, `[:find ?v :where [?e :test/val ?v] [(is-positive ?v)] [(is-even ?v)]]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(qr.Rows) != 1 || qr.Rows[0][0] != int64(4) {
		t.Errorf("expected [4], got %v", qr.Rows)
	}
}
