# litelog

A high-performance, Datalog-queryable bitemporal datastore built on SQLite. litelog implements a Datomic-style Entity-Attribute-Value (EAV) data model with Datalog queries compiled to SQL, first-class bitemporal support, recursive query evaluation via semi-naive fixpoint, and a pure Go implementation (no CGo).

Built on [zombiezen.com/go/sqlite](https://github.com/nicksantos/zombiezen-sqlite) (pure Go SQLite via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite)).

## Features

- **Datomic-style EAV model** -- facts are stored as datoms (entity, attribute, value, transaction, added)
- **Datalog query language** -- parsed from EDN, algebrized, and compiled to SQL
- **Bitemporal** -- every fact tracks both transaction time (system) and valid time (business); query at any point in either timeline
- **Recursive queries** -- rule-based Datalog with semi-naive evaluation
- **Append-only history** -- full audit trail; nothing is ever deleted from the log
- **Pure Go** -- no CGo dependency; uses modernc.org/sqlite
- **Connection pooling** -- concurrent reads via sqlitex.Pool
- **Materialized current state** -- `current_datoms` table for fast present-time queries
- **User-defined functions** -- register Go functions as Datalog predicates via `RegisterFunction`
- **Hot/cold partitioning** -- separate current state from history into independent SQLite files

## Quick Start

```go
package main

import (
    "context"
    "fmt"
    "github.com/loosh-industries/litelog"
)

func main() {
    db, _ := litelog.Open(":memory:")
    defer db.Close()
    ctx := context.Background()

    // Define schema
    db.DefineAttribute(ctx, litelog.AttributeSchema{
        Ident:       ":person/name",
        ValueType:   litelog.TypeString,
        Cardinality: litelog.CardOne,
    })
    db.DefineAttribute(ctx, litelog.AttributeSchema{
        Ident:       ":person/age",
        ValueType:   litelog.TypeInt,
        Cardinality: litelog.CardOne,
    })

    // Transact data
    tx, _ := db.Transact(ctx, []litelog.TxDatom{
        {E: litelog.TempID("alice"), A: ":person/name", V: "Alice"},
        {E: litelog.TempID("alice"), A: ":person/age", V: int64(30)},
        {E: litelog.TempID("bob"), A: ":person/name", V: "Bob"},
        {E: litelog.TempID("bob"), A: ":person/age", V: int64(25)},
    })

    // Query with Datalog
    result, _ := db.Query(ctx, `[:find ?name ?age :where [?e :person/name ?name] [?e :person/age ?age]]`)
    for _, row := range result.Rows {
        fmt.Println(row[0], row[1])
    }

    // Time travel -- see the database as of a specific transaction
    result, _ = db.AsOf(tx.TxID).Query(ctx, `[:find ?name :where [?e :person/name ?name]]`)
    for _, row := range result.Rows {
        fmt.Println(row[0])
    }
}
```

## Concepts

### EAV Model

litelog stores all data as **datoms** -- immutable facts of the form `(entity, attribute, value, transaction, added)`. An entity is an integer ID. An attribute is a named, typed schema element (interned to an integer ID internally). A value is typed according to its attribute's schema. Every datom records which transaction produced it, and whether it was asserted (`added=true`) or retracted (`added=false`).

### Attributes

Attributes are typed, named schema elements registered before use. Each attribute has:

- **Ident** -- a namespaced keyword string (e.g., `":person/name"`)
- **ValueType** -- the type of values this attribute holds
- **Cardinality** -- whether an entity can have one or many values for this attribute

Attribute idents are interned to integer IDs at definition time. All internal comparisons use the integer ID, not the string.

### Transactions

All writes happen through transactions -- atomic batches of assertions and retractions. Each transaction is assigned a monotonically increasing ID and a timestamp. Transactions are logged in the `transactions` table and every datom records its originating transaction ID.

### TempIDs

`TempID` is a string-typed placeholder used in transactions to refer to entities that do not yet have a permanent ID. All datoms sharing the same TempID within a transaction resolve to the same new entity ID. The resolved mappings are returned in `TxResult.Tempids`.

### Bitemporality

litelog tracks two time dimensions for every fact:

- **Transaction time** (`tx`) -- when the fact was recorded in the system. Monotonically increasing; immutable once written.
- **Valid time** (`valid_from`, `valid_to`) -- when the fact is considered true in the business domain. Can be set to any time, including the past or future, using `TransactWithValidTime`.

This enables queries like "what did we know about X at time T?" (transaction time) and "what was true about X at time T?" (valid time).

## Schema Definition

### Value Types

| Constant     | Go Type     | Storage              |
|-------------|------------|----------------------|
| `TypeInt`    | `int64`     | 8-byte big-endian    |
| `TypeString` | `string`    | raw bytes            |
| `TypeFloat`  | `float64`   | order-preserving BE  |
| `TypeBool`   | `bool`      | single byte (0/1)    |
| `TypeRef`    | `int64`     | 8-byte big-endian    |
| `TypeBytes`  | `[]byte`    | raw bytes            |
| `TypeInst`   | `time.Time` | Unix ms, big-endian  |

### Cardinality

| Constant   | Behavior |
|-----------|----------|
| `CardOne`  | Entity holds at most one value per attribute. Asserting a new value automatically retracts the previous one. |
| `CardMany` | Entity holds a set of values. Each assertion adds to the set; explicit retraction removes. |

### Options

| Field         | Effect |
|--------------|--------|
| `Indexed`     | Creates an AVET index entry for fast value lookups |
| `Unique`      | Enforces uniqueness on the attribute's value across all entities |
| `IsComponent` | Marks the attribute as a component reference (for future cascade-delete support) |

### Idempotent Definitions

Calling `DefineAttribute` with an identical schema to an existing attribute is a no-op. Calling it with a conflicting schema returns an error.

```go
db.DefineAttribute(ctx, litelog.AttributeSchema{
    Ident:       ":order/amount",
    ValueType:   litelog.TypeFloat,
    Cardinality: litelog.CardOne,
    Indexed:     true,
})
```

## Transactions

### Transact

`Transact` applies a batch of `TxDatom` values atomically within an immediate SQLite transaction.

```go
tx, err := db.Transact(ctx, []litelog.TxDatom{
    {E: litelog.TempID("e1"), A: ":person/name", V: "Alice"},
    {E: litelog.TempID("e1"), A: ":person/age", V: int64(30)},
})
```

### TransactWithValidTime

Asserts facts at a specific valid time instead of the transaction time.

```go
pastTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
tx, err := db.TransactWithValidTime(ctx, datoms, pastTime)
```

### TxDatom Structure

```go
type TxDatom struct {
    E       any    // int64 entity ID or TempID
    A       string // attribute ident, e.g. ":person/name"
    V       any    // value (must match attribute's ValueType)
    Retract bool   // true = retract this fact
}
```

- **E** -- either a `TempID("label")` for new entities or an `int64` for existing ones
- **A** -- the attribute ident string (must be previously defined)
- **V** -- the value, whose Go type must match the attribute's ValueType
- **Retract** -- when true, removes the specific (E, A, V) triple from current state

### CardOne Automatic Retraction

For `CardOne` attributes, asserting a new value for an entity automatically retracts the previous value. The retraction datom is recorded in the history log and included in `TxResult.Datoms`.

### Explicit Retractions

Set `Retract: true` to remove a specific fact:

```go
db.Transact(ctx, []litelog.TxDatom{
    {E: int64(1001), A: ":person/email", V: "old@example.com", Retract: true},
})
```

### TxResult

```go
type TxResult struct {
    TxID    int64            // the transaction's entity ID
    TxTime  int64            // Unix milliseconds when the transaction occurred
    Tempids map[TempID]int64 // maps each TempID to its resolved entity ID
    Datoms  []Datom          // all datoms asserted and retracted in this transaction
}
```

## Query Language

litelog uses a Datomic-style Datalog query language, parsed from EDN (Extensible Data Notation) strings.

### Basic Structure

```
[:find <find-elems>
 :in <inputs>
 :where <clauses>
 :order-by <orderings>]
```

All clauses except `:find` and `:where` are optional.

### :find Clause

Specifies which variables to return. Supports plain variables and aggregates.

```clojure
;; Plain variables
[:find ?name ?age :where ...]

;; Aggregates
[:find ?name (count ?e) (sum ?amount) :where ...]
```

Supported aggregates: `count`, `sum`, `avg`, `min`, `max`.

### :where Clause

Contains data patterns and expression clauses that constrain which datoms match.

**Data patterns** match against the datom store:

```clojure
[?e :person/name ?name]         ;; bind entity and value
[?e :person/age ?age]           ;; same entity, different attribute
[?e :person/name "Alice"]       ;; constant value
[?e :order/item ?item ?tx]      ;; also bind transaction ID
[_ :person/name ?name]          ;; blank entity (any entity)
```

**Expression clauses** filter with predicates:

```clojure
[(> ?age 21)]                   ;; comparison
[(<= ?price 100)]
[(= ?status "active")]
[(!= ?name "test")]
```

Supported operators: `>`, `<`, `>=`, `<=`, `=`, `!=`.

**not clauses** exclude matching patterns:

```clojure
(not [?e :person/retired true])
```

Compiles to `NOT EXISTS` subqueries with correlated joins on shared variables.

**or clauses** match any alternative:

```clojure
(or [?e :person/role "admin"]
    [?e :person/role "superuser"])
```

Compiles to `UNION ALL` queries.

### :in Clause

Parameterizes queries with external values. The implicit `$` source is always present.

```go
result, err := db.Query(ctx,
    `[:find ?name :in $ ?min-age :where [?e :person/name ?name] [?e :person/age ?age] [(>= ?age ?min-age)]]`,
    int64(21),
)
```

### :order-by Clause

Orders results by one or more variables, ascending by default.

```clojure
[:find ?name ?age
 :where [?e :person/name ?name] [?e :person/age ?age]
 :order-by [?age :desc] ?name]
```

### Pattern Elements

| Element       | Example              | Meaning |
|--------------|---------------------|---------|
| Variable      | `?x`, `?name`        | Binds to matched values; shared across patterns for joins |
| Keyword       | `:person/name`       | Attribute ident constant |
| Integer       | `42`                 | Integer constant |
| String        | `"Alice"`            | String constant |
| Boolean       | `true`, `false`      | Boolean constant |
| Blank         | `_`                  | Matches anything, does not bind |

## Temporal Queries

### AsOf (Transaction Time)

See the database as it existed at a specific transaction. Queries the full `datoms` history table, filtering to assertions (`added=1`) with `tx <= txID`.

```go
view := db.AsOf(txID)
result, err := view.Query(ctx, `[:find ?name :where [?e :person/name ?name]]`)
```

### AsOfValidTime (Valid Time)

See facts that were valid at a specific point in time. Filters on `valid_from <= t` and `COALESCE(valid_to, MAX_INT) > t`.

```go
view := db.AsOfValidTime(time.Date(2023, 6, 15, 0, 0, 0, 0, time.UTC))
result, err := view.Query(ctx, `[:find ?name :where [?e :person/name ?name]]`)
```

### TransactWithValidTime

Assert facts with a specific valid-from time, independent of when the transaction is recorded.

```go
// Record that Alice's salary was $80k starting Jan 2023
db.TransactWithValidTime(ctx, []litelog.TxDatom{
    {E: aliceID, A: ":employee/salary", V: int64(80000)},
}, time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC))
```

### How Bitemporality Works

Every datom in the `datoms` table carries:

| Column       | Meaning |
|-------------|---------|
| `tx`         | Transaction ID -- when the system recorded this fact |
| `valid_from` | Start of the fact's validity period (Unix ms) |
| `valid_to`   | End of the fact's validity period (Unix ms; NULL = still valid) |

The `current_datoms` table is a materialized view of the latest state, used for present-time queries. Temporal queries bypass `current_datoms` and read directly from `datoms` with appropriate filters.

## Recursive Queries

litelog supports recursive Datalog rules evaluated via semi-naive fixpoint iteration.

### Rule Syntax

Rules are defined as EDN vectors with a head (a list) and body clauses:

```clojure
;; Base case: direct parent relationship
[(ancestor ?a ?d) [?a :person/parent ?d]]

;; Recursive case: transitive closure
[(ancestor ?a ?d) [?a :person/parent ?p] (ancestor ?p ?d)]
```

### Transitive Closure Example

```go
import "github.com/loosh-industries/litelog/query"

// Parse rules
rules, err := query.ParseRules(`[
    [(ancestor ?a ?d) [?a :person/parent ?d]]
    [(ancestor ?a ?d) [?a :person/parent ?p] (ancestor ?p ?d)]
]`)

// Plan recursive evaluation
plan, err := query.PlanRecursive(rules, schemaLookup)

// Execute the plan:
// 1. Run plan.SetupSQL   -- create temp tables (_rule_ancestor, _delta_ancestor)
// 2. Run plan.BaseSQL    -- populate initial facts from non-recursive rules
// 3. Loop plan.DeltaSQL  -- iterate until no new rows are produced (fixpoint)
// 4. Query plan.ResultTable for results
// 5. Run plan.CleanupSQL -- drop temp tables
```

### Semi-Naive Evaluation

The recursive evaluator uses semi-naive evaluation for efficiency:

1. **Base case**: non-recursive rule bodies populate the derived table directly
2. **Delta iteration**: each iteration only joins *newly discovered* facts (from `_delta_` tables) with existing facts, avoiding redundant re-derivation
3. **Fixpoint**: iteration stops when a delta round produces no new rows
4. **Deduplication**: `INSERT OR IGNORE` with `PRIMARY KEY` constraints ensures each derived fact is stored at most once

### API

- `query.ParseRules(input string) ([]*Rule, error)` -- parse multiple rules from an EDN vector
- `query.ParseRule(input string) (*Rule, error)` -- parse a single rule
- `query.PlanRecursive(rules []*Rule, schema SchemaLookup) (*RecursivePlan, error)` -- generate the execution plan
- `query.ParseRulesAndQuery(rulesInput, queryInput string) ([]*Rule, *Query, error)` -- parse both rules and a query

## Architecture

### Storage

litelog uses SQLite in WAL mode with four core tables:

| Table             | Purpose |
|------------------|---------|
| `datoms`          | Append-only history log of all datoms ever asserted or retracted |
| `current_datoms`  | Materialized current state -- only the latest assertions, keyed by `(e, a, v)` |
| `transactions`    | Transaction log with ID and timestamp |
| `attributes`      | Attribute registry (ident, type, cardinality, options) |

### Indexes

| Index                    | Columns                        | Purpose |
|-------------------------|-------------------------------|---------|
| `idx_eavt`               | `(e, a, v, tx)`               | Entity-centric access (current) |
| `idx_aevt`               | `(a, e, v, tx)`               | Attribute-centric access (current) |
| `idx_avet`               | `(a, v, e, tx)`               | Value lookup for indexed attributes (current) |
| `idx_vaet`               | `(v, a, e, tx)` WHERE ref     | Reverse navigation for ref-type attributes (current) |
| `idx_datoms_tx`          | `(tx)`                        | Transaction-time access on history |
| `idx_datoms_temporal`    | `(e, a, valid_from, tx)`      | Bitemporal range queries on history |
| `idx_datoms_eavt`        | `(e, a, v, tx)`               | EAVT on history for as-of queries |

The query algebrizer automatically selects index hints based on which pattern elements are bound.

### Query Pipeline

```
EDN string
    |
    v
 Parse (edn/ + query/parser.go)
    |  produces query.Query AST
    v
 Algebrize (query/algebrize.go)
    |  resolves attributes, builds table bindings,
    |  infers joins, resolves filters
    |  produces query.AlgebraicQuery
    v
 Translate (query/translate.go)
    |  generates SQL with positional parameters
    |  produces query.SQLQuery
    v
 Execute (query_api.go)
    |  binds parameters, decodes BLOB values
    v
 QueryResult{Columns, Rows}
```

### Value Encoding

All values are stored as BLOBs in SQLite with order-preserving encodings:

- **Integers and refs**: 8-byte big-endian (preserves sort order for positive values)
- **Floats**: IEEE 754 bits with sign-bit manipulation so lexicographic byte comparison matches numeric ordering
- **Strings**: raw UTF-8 bytes (natural lexicographic order)
- **Booleans**: single byte (`0x00` or `0x01`)
- **Instants**: Unix milliseconds encoded as big-endian int64
- **Bytes**: stored as-is

This encoding allows SQLite's native BLOB comparison operators to produce semantically correct results for all types, enabling the generated SQL to use plain `>`, `<`, `=` operators on the `v` column.

### Connection Management

- **Pool**: `sqlitex.Pool` manages a configurable number of connections (default 10)
- **Reads**: any connection from the pool via `withConn`
- **Writes**: `withWriteConn` wraps operations in `sqlitex.ImmediateTransaction` to avoid WAL deadlocks
- **Pragmas**: each connection is initialized with performance-tuned settings:
  - `journal_mode=WAL`
  - `synchronous=NORMAL`
  - `cache_size=-64000` (64 MB)
  - `mmap_size=268435456` (256 MB)
  - `temp_store=MEMORY`

## Performance

- **Pure Go, no CGo** -- builds anywhere Go builds, via modernc.org/sqlite
- **Statement caching** -- prepared statements are reused within connections via `conn.Prepare`
- **Order-preserving BLOB encoding** -- enables index-based comparisons directly in SQL without function calls
- **Materialized current state** -- `current_datoms` avoids scanning the full history for present-time queries
- **Attribute interning** -- string idents are resolved to integer IDs once; all storage and comparisons use the integer
- **Connection pool** -- concurrent reads scale across pool connections
- **Immediate transactions** -- writes acquire the WAL lock up front, avoiding deadlocks under contention
- **Index hints** -- the algebrizer emits `INDEXED BY` hints to guide SQLite's query planner based on bound pattern elements

## Pull API

The Pull API provides entity-centric data retrieval -- fetch all or selected attributes for an entity in a single call, with optional nested traversal of ref-type attributes.

### PullPattern

`PullPattern` controls which attributes are retrieved:

```go
// All attributes (wildcard)
pattern := litelog.PullWildcard()

// Specific attributes
pattern := litelog.PullAttrs(":person/name", ":person/age")

// Nested ref traversal
friendPattern := litelog.PullAttrs(":person/name")
pattern := litelog.PullPattern{
    Attrs: []litelog.PullAttr{
        {Ident: ":person/name"},
        {Ident: ":person/best-friend", Nested: &friendPattern},
    },
}

// Aliased keys
pattern := litelog.PullPattern{
    Attrs: []litelog.PullAttr{
        {Ident: ":person/name", AsAlias: "name"},
    },
}
```

### Pull / PullMany

```go
// Single entity
result, err := db.Pull(ctx, entityID, pattern)
// result is PullResult (map[string]any)
// Always includes ":db/id" with the entity ID

// Multiple entities
results, err := db.PullMany(ctx, []int64{id1, id2}, pattern)
```

### Behavior

- **CardOne** attributes produce a single value
- **CardMany** attributes produce `[]any`
- **Ref-type** attributes with `Nested` pattern produce nested `PullResult` (or `[]PullResult` for CardMany refs)
- **Wildcard + refs**: follows refs recursively with the same wildcard pattern
- **Depth limit**: recursion capped at 16 levels; circular refs degrade gracefully to raw entity IDs
- **Temporal**: `db.AsOf(txID).Pull(...)` and `db.AsOfValidTime(t).Pull(...)` work on temporal views

## Type-Specialized Columns

Both `datoms` and `current_datoms` tables include type-specialized columns alongside the canonical BLOB `v` column:

| Column    | SQLite Type | Used By                          |
|-----------|------------|----------------------------------|
| `v_int`   | INTEGER    | TypeInt, TypeRef, TypeBool, TypeInst |
| `v_text`  | TEXT       | TypeString                       |
| `v_float` | REAL       | TypeFloat                        |
| `v`       | BLOB       | TypeBytes + canonical storage    |

### How It Works

- **Writes**: `Transact` populates both the BLOB `v` column and the appropriate typed column
- **Reads**: queries and Pull API read from typed columns when the value type is known, skipping BLOB decode
- **Filters**: WHERE clause comparisons use typed columns for native SQLite comparison (no BLOB encoding overhead)
- **Migration**: existing databases are automatically migrated — typed columns are added as nullable columns

### Benefits

- **Faster queries**: native `INTEGER > ?` comparison vs BLOB byte comparison
- **Faster decoding**: read `int64`/`text`/`float64` directly from SQLite, skip `decodeValue`
- **Better debugging**: raw SQL queries show human-readable values in typed columns
- **Full backward compatibility**: BLOB `v` column retained for unique constraints and TypeBytes

## User-Defined Functions (UDFs)

Register custom Go functions that are callable from Datalog queries as predicates or value-producing functions.

### Registration

```go
// Boolean predicate: filter rows where age >= 18
db.RegisterFunction("is-adult", 1, true, func(args ...any) (any, error) {
    age, ok := args[0].(int64)
    if !ok {
        return false, nil
    }
    return age >= 18, nil
})

// Multi-arg predicate: check if point is near origin
db.RegisterFunction("near-origin", 2, true, func(args ...any) (any, error) {
    x, _ := args[0].(float64)
    y, _ := args[1].(float64)
    dist := math.Sqrt(x*x + y*y)
    return dist < 5.0, nil
})
```

Parameters:
- **name**: function name (hyphenated names like `is-adult` supported)
- **nArgs**: number of arguments (-1 for variadic)
- **deterministic**: `true` if same inputs always produce same output (enables SQLite optimization)
- **fn**: `func(args ...any) (any, error)` — arguments are decoded Go values

### Query Usage

UDFs are called from Datalog `where` clauses using the standard function-call syntax:

```go
// Single-arg boolean predicate
qr, _ := db.Query(ctx, `[:find ?name :where
    [?e :person/name ?name]
    [?e :person/age ?age]
    [(is-adult ?age)]]`)

// Multi-arg predicate
qr, _ := db.Query(ctx, `[:find ?x ?y :where
    [?e :point/x ?x]
    [?e :point/y ?y]
    [(near-origin ?x ?y)]]`)

// Combine multiple UDFs
qr, _ := db.Query(ctx, `[:find ?v :where
    [?e :test/val ?v]
    [(is-positive ?v)]
    [(is-even ?v)]]`)
```

### How It Works

- UDFs are registered in a thread-safe registry on the `DB` handle
- Connections lazily install UDFs via `sqlite.Conn.CreateFunction` when first used after registration
- Hyphenated function names are double-quoted in generated SQL (`"is-adult"(...)`)
- Boolean predicates returning `true`/`false` are converted to SQLite integers `1`/`0`
- Supported return types: `bool`, `int64`, `int`, `float64`, `string`, `[]byte`, `nil`

## Hot/Cold Database Partitioning

Split history from current state into separate SQLite files. Current-state queries hit only the small hot database; historical queries (AsOf, AsOfValidTime) read from the cold history file.

### Setup

```go
db, _ := litelog.Open("current.db", litelog.Options{
    PoolSize:    10,
    HistoryPath: "history.db",
})
defer db.Close()
```

When `HistoryPath` is set:
- `current.db` holds `current_datoms`, `attributes`, `transactions` — the hot working set
- `history.db` holds `datoms` — the full append-only history log
- Both files are created automatically on first open
- The history DB is ATTACHed on each pool connection

### Behavior

- **Transact** writes to both: appends to `history.datoms`, upserts `current_datoms`
- **Query** (current state) reads only `current_datoms` — no change from single-file mode
- **AsOf / AsOfValidTime** reads from `history.datoms`
- **Pull** reads from `current_datoms`; temporal Pull reads from `history.datoms`
- **UDFs** work identically in both modes
- `db.Partitioned()` returns `true` when hot/cold is enabled

### Benefits

- **Smaller hot DB**: current state stays compact as history grows
- **Better cache hit ratio**: SQLite page cache focuses on active data
- **Independent backup/archival**: history can be backed up or moved to cold storage separately
- **Gradual adoption**: omit `HistoryPath` to keep single-file behavior (default)

### Reopening

Pass the same `HistoryPath` when reopening — the ATTACH happens automatically per-connection:

```go
db, _ := litelog.Open("current.db", litelog.Options{
    HistoryPath: "history.db",
})
```

## Future Enhancements

- Attribute-based sharding
- Blob I/O for large values
- Changeset-based replication

## API Reference

### Core Types

| Type              | Description |
|------------------|-------------|
| `DB`              | Top-level database handle; holds connection pool and schema cache |
| `Options`         | Configuration for `Open` (pool size, history path) |
| `DBView`          | Read-only temporal view of the database (from `AsOf`/`AsOfValidTime`) |
| `AttributeSchema` | Schema definition for an attribute (ident, type, cardinality, options) |
| `Datom`           | A single fact: entity, attribute (interned ID), value, tx, added |
| `TxDatom`         | Transaction input: entity (ID or TempID), attribute ident, value, retract flag |
| `TempID`          | Placeholder entity ID resolved during transaction |
| `TxResult`        | Transaction outcome: tx ID, timestamp, resolved tempids, produced datoms |
| `QueryResult`     | Query result: column names and decoded rows |
| `PullPattern`     | Specifies which attributes to retrieve in a Pull call |
| `PullAttr`        | Single attribute spec with optional nested pattern and alias |
| `PullResult`      | `map[string]any` — attribute ident to decoded value |
| `ValueType`       | Enum: `TypeInt`, `TypeString`, `TypeFloat`, `TypeBool`, `TypeRef`, `TypeBytes`, `TypeInst` |
| `Cardinality`     | Enum: `CardOne`, `CardMany` |
| `UDFFunc`         | `func(args ...any) (any, error)` — user-defined function signature |
| `UDFDef`          | Registered UDF definition: name, nargs, deterministic, fn |

### Core Functions

| Function                                | Description |
|----------------------------------------|-------------|
| `Open(path string, opts ...Options) (*DB, error)` | Open or create a database at the given path |
| `(*DB).Close() error`                   | Close all connections |
| `(*DB).DefineAttribute(ctx, schema) error` | Register an attribute in the schema |
| `(*DB).Transact(ctx, datoms) (*TxResult, error)` | Apply a batch of datoms atomically |
| `(*DB).TransactWithValidTime(ctx, datoms, t) (*TxResult, error)` | Transact with explicit valid time |
| `(*DB).Query(ctx, datalog, args...) (*QueryResult, error)` | Execute a Datalog query against current state |
| `(*DB).AsOf(txID) *DBView`              | Create a transaction-time view |
| `(*DB).AsOfValidTime(t) *DBView`        | Create a valid-time view |
| `(*DBView).Query(ctx, datalog, args...) (*QueryResult, error)` | Query against a temporal view |
| `(*DB).Pull(ctx, eid, pattern) (PullResult, error)` | Pull attributes for a single entity |
| `(*DB).PullMany(ctx, eids, pattern) ([]PullResult, error)` | Pull attributes for multiple entities |
| `(*DBView).Pull(ctx, eid, pattern) (PullResult, error)` | Pull from a temporal view |
| `PullWildcard() PullPattern`            | Pattern that retrieves all attributes |
| `PullAttrs(idents...) PullPattern`      | Pattern for specific attributes |
| `(*DB).RegisterFunction(name, nArgs, deterministic, fn) error` | Register a UDF callable from Datalog queries |
| `(*DB).Partitioned() bool`              | Returns true if hot/cold partitioning is enabled |

### Schema Cache

| Function                                | Description |
|----------------------------------------|-------------|
| `(*SchemaCache).GetByIdent(ident) *CachedAttribute` | Look up attribute by ident string |
| `(*SchemaCache).GetByID(id) *CachedAttribute` | Look up attribute by interned ID |
| `(*SchemaCache).All() []*CachedAttribute` | Return all cached attributes |

### Query Package (`github.com/loosh-industries/litelog/query`)

| Function                                | Description |
|----------------------------------------|-------------|
| `ParseQuery(input) (*Query, error)`     | Parse a Datalog query from EDN |
| `ParseRule(input) (*Rule, error)`       | Parse a single Datalog rule from EDN |
| `ParseRules(input) ([]*Rule, error)`    | Parse multiple rules from EDN |
| `ParseRulesAndQuery(rules, query) ([]*Rule, *Query, error)` | Parse both rules and a query |
| `Algebrize(q, schema) (*AlgebraicQuery, error)` | Convert parsed query to algebraic form |
| `Translate(aq) (*SQLQuery, error)`      | Convert algebraic query to executable SQL |
| `PlanRecursive(rules, schema) (*RecursivePlan, error)` | Generate execution plan for recursive rules |

### EDN Package (`github.com/loosh-industries/litelog/edn`)

| Function                                | Description |
|----------------------------------------|-------------|
| `Parse(input) (Node, error)`            | Parse an EDN string into an AST |
| `Node.IsVariable() bool`               | Check if the node is a `?`-prefixed symbol |
| `Node.AsKeyword() string`              | Get keyword value without leading colon |
| `Node.AsSymbol() string`               | Get symbol value |
| `Node.AsInt() int64`                   | Get parsed integer value |
| `Node.AsString() string`               | Get string value |
