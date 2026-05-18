package litelog

import (
	"context"
	"testing"
)

func TestPull_Wildcard(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	tx := transactAliceBob(t, db)

	aliceID := tx.Tempids[TempID("alice")]
	result, err := db.Pull(context.Background(), aliceID, PullWildcard())
	if err != nil {
		t.Fatalf("Pull wildcard: %v", err)
	}

	if result[":db/id"] != aliceID {
		t.Errorf("expected :db/id=%d, got %v", aliceID, result[":db/id"])
	}
	if result[":person/name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", result[":person/name"])
	}
	if result[":person/age"] != int64(30) {
		t.Errorf("expected age=30, got %v", result[":person/age"])
	}
}

func TestPull_SpecificAttrs(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	tx := transactAliceBob(t, db)

	aliceID := tx.Tempids[TempID("alice")]
	result, err := db.Pull(context.Background(), aliceID, PullAttrs(":person/name"))
	if err != nil {
		t.Fatalf("Pull specific: %v", err)
	}

	if result[":person/name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", result[":person/name"])
	}
	if _, has := result[":person/age"]; has {
		t.Error("should not have :person/age when not requested")
	}
}

func TestPull_NestedRef(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":person/name", ValueType: TypeString, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":person/best-friend", ValueType: TypeRef, Cardinality: CardOne,
	}))

	tx1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
		{E: TempID("bob"), A: ":person/name", V: "Bob"},
	})
	if err != nil {
		t.Fatal(err)
	}
	aliceID := tx1.Tempids[TempID("alice")]
	bobID := tx1.Tempids[TempID("bob")]

	_, err = db.Transact(ctx, []TxDatom{
		{E: aliceID, A: ":person/best-friend", V: bobID},
	})
	if err != nil {
		t.Fatal(err)
	}
	friendPattern := PullAttrs(":person/name")
	result, err := db.Pull(ctx, aliceID, PullPattern{
		Attrs: []PullAttr{
			{Ident: ":person/name"},
			{Ident: ":person/best-friend", Nested: &friendPattern},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	friend, ok := result[":person/best-friend"].(PullResult)
	if !ok {
		t.Fatalf("expected nested PullResult, got %T: %v", result[":person/best-friend"], result[":person/best-friend"])
	}
	if friend[":person/name"] != "Bob" {
		t.Errorf("expected friend name=Bob, got %v", friend[":person/name"])
	}
}

func TestPull_CardMany(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":person/name", ValueType: TypeString, Cardinality: CardOne,
	}))
	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":person/tag", ValueType: TypeString, Cardinality: CardMany,
	}))

	tx, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
		{E: TempID("alice"), A: ":person/tag", V: "admin"},
		{E: TempID("alice"), A: ":person/tag", V: "developer"},
	})
	if err != nil {
		t.Fatal(err)
	}

	aliceID := tx.Tempids[TempID("alice")]
	result, err := db.Pull(ctx, aliceID, PullWildcard())
	if err != nil {
		t.Fatal(err)
	}

	tags, ok := result[":person/tag"].([]any)
	if !ok {
		t.Fatalf("expected []any for CardMany, got %T", result[":person/tag"])
	}
	if len(tags) != 2 {
		t.Fatalf("expected 2 tags, got %d", len(tags))
	}

	tagSet := map[string]bool{}
	for _, tg := range tags {
		tagSet[tg.(string)] = true
	}
	if !tagSet["admin"] || !tagSet["developer"] {
		t.Errorf("expected admin+developer tags, got %v", tags)
	}
}

func TestPullMany(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	tx := transactAliceBob(t, db)

	aliceID := tx.Tempids[TempID("alice")]
	bobID := tx.Tempids[TempID("bob")]

	results, err := db.PullMany(context.Background(), []int64{aliceID, bobID}, PullAttrs(":person/name"))
	if err != nil {
		t.Fatal(err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0][":person/name"] != "Alice" {
		t.Errorf("expected Alice, got %v", results[0][":person/name"])
	}
	if results[1][":person/name"] != "Bob" {
		t.Errorf("expected Bob, got %v", results[1][":person/name"])
	}
}

func TestPull_NonexistentEntity(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)

	result, err := db.Pull(context.Background(), 999999, PullWildcard())
	if err != nil {
		t.Fatal(err)
	}
	// Should just have :db/id
	if len(result) != 1 {
		t.Errorf("expected only :db/id for nonexistent entity, got %d keys", len(result))
	}
}

func TestPull_Alias(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	tx := transactAliceBob(t, db)

	aliceID := tx.Tempids[TempID("alice")]
	result, err := db.Pull(context.Background(), aliceID, PullPattern{
		Attrs: []PullAttr{
			{Ident: ":person/name", AsAlias: "name"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if result["name"] != "Alice" {
		t.Errorf("expected aliased name=Alice, got %v", result["name"])
	}
	if _, has := result[":person/name"]; has {
		t.Error("should use alias, not original ident")
	}
}

func TestPull_DepthLimit(t *testing.T) {
	db := openMem(t)
	ctx := context.Background()

	must(t, db.DefineAttribute(ctx, AttributeSchema{
		Ident: ":node/next", ValueType: TypeRef, Cardinality: CardOne,
	}))

	// Create two nodes, then link them circularly
	tx1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("a"), A: ":node/next", V: int64(0)}, // placeholder
		{E: TempID("b"), A: ":node/next", V: int64(0)}, // placeholder
	})
	if err != nil {
		t.Fatal(err)
	}
	aID := tx1.Tempids[TempID("a")]
	bID := tx1.Tempids[TempID("b")]

	// A -> B
	_, err = db.Transact(ctx, []TxDatom{
		{E: aID, A: ":node/next", V: bID},
	})
	if err != nil {
		t.Fatal(err)
	}
	// B -> A (circular)
	_, err = db.Transact(ctx, []TxDatom{
		{E: bID, A: ":node/next", V: aID},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Wildcard pull should terminate (graceful fallback to raw ID at depth limit)
	result, err := db.Pull(ctx, aID, PullWildcard())
	if err != nil {
		t.Fatalf("Pull with circular ref should gracefully degrade, got: %v", err)
	}
	if result[":db/id"] != aID {
		t.Errorf("expected :db/id=%d, got %v", aID, result[":db/id"])
	}
}

func TestPull_AsOfView(t *testing.T) {
	db := openMem(t)
	definePersonAttrs(t, db)
	ctx := context.Background()

	tx1, err := db.Transact(ctx, []TxDatom{
		{E: TempID("alice"), A: ":person/name", V: "Alice"},
		{E: TempID("alice"), A: ":person/age", V: int64(30)},
	})
	if err != nil {
		t.Fatal(err)
	}
	aliceID := tx1.Tempids[TempID("alice")]

	// Update age
	_, err = db.Transact(ctx, []TxDatom{
		{E: aliceID, A: ":person/age", V: int64(31)},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Current pull should see age=31
	current, err := db.Pull(ctx, aliceID, PullAttrs(":person/age"))
	if err != nil {
		t.Fatal(err)
	}
	if current[":person/age"] != int64(31) {
		t.Errorf("expected current age=31, got %v", current[":person/age"])
	}

	// AsOf tx1 should see age=30
	historical, err := db.AsOf(tx1.TxID).Pull(ctx, aliceID, PullAttrs(":person/age"))
	if err != nil {
		t.Fatal(err)
	}
	if historical[":person/age"] != int64(30) {
		t.Errorf("expected historical age=30, got %v", historical[":person/age"])
	}
}
