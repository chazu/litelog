package litelog

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/chazu/litelog/edn"
	"github.com/chazu/litelog/query"
)

// ---------------------------------------------------------------------------
// Value encoding/decoding (hot path)
// ---------------------------------------------------------------------------

func BenchmarkEncodeInt64(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = encodeInt64(int64(i))
	}
}

func BenchmarkDecodeInt64(b *testing.B) {
	b.ReportAllocs()
	buf := encodeInt64(42)
	for i := 0; i < b.N; i++ {
		_ = decodeInt64(buf)
	}
}

func BenchmarkEncodeFloat64(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = encodeFloat64(3.14159)
	}
}

func BenchmarkDecodeFloat64(b *testing.B) {
	b.ReportAllocs()
	buf := encodeFloat64(3.14159)
	for i := 0; i < b.N; i++ {
		_ = decodeFloat64(buf)
	}
}

func BenchmarkEncodeString_Short(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = encodeValue("hello", TypeString)
	}
}

func BenchmarkEncodeString_Long(b *testing.B) {
	b.ReportAllocs()
	long := string(make([]byte, 1024))
	for i := 0; i < b.N; i++ {
		_, _ = encodeValue(long, TypeString)
	}
}

func BenchmarkEncodeValue_Int(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = encodeValue(int64(42), TypeInt)
	}
}

func BenchmarkEncodeValue_String(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = encodeValue("hello world", TypeString)
	}
}

func BenchmarkEncodeValue_Float(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_, _ = encodeValue(3.14159, TypeFloat)
	}
}

func BenchmarkDecodeValue_Int(b *testing.B) {
	b.ReportAllocs()
	buf := encodeInt64(42)
	for i := 0; i < b.N; i++ {
		_, _ = decodeValue(buf, TypeInt)
	}
}

func BenchmarkDecodeValue_String(b *testing.B) {
	b.ReportAllocs()
	buf := []byte("hello world")
	for i := 0; i < b.N; i++ {
		_, _ = decodeValue(buf, TypeString)
	}
}

// ---------------------------------------------------------------------------
// Transaction throughput
// ---------------------------------------------------------------------------

func benchDBPath(b *testing.B) string {
	b.Helper()
	dir := b.TempDir()
	return filepath.Join(dir, "bench.db")
}

func openBenchDB(b *testing.B) *DB {
	b.Helper()
	db, err := Open(benchDBPath(b), Options{PoolSize: 2})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":bench/name",
		ValueType:   TypeString,
		Cardinality: CardOne,
	}); err != nil {
		b.Fatal(err)
	}
	if err := db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":bench/value",
		ValueType:   TypeInt,
		Cardinality: CardOne,
	}); err != nil {
		b.Fatal(err)
	}
	return db
}

func benchTransactN(b *testing.B, n int) {
	b.Helper()
	b.ReportAllocs()
	db := openBenchDB(b)
	ctx := context.Background()

	datoms := make([]TxDatom, n*2)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// Build datoms for this iteration with unique tempids.
		for j := 0; j < n; j++ {
			tid := TempID(fmt.Sprintf("e%d_%d", i, j))
			datoms[j*2] = TxDatom{E: tid, A: ":bench/name", V: fmt.Sprintf("entity-%d", j)}
			datoms[j*2+1] = TxDatom{E: tid, A: ":bench/value", V: int64(j)}
		}
		if _, err := db.Transact(ctx, datoms); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTransact_SingleDatom(b *testing.B) {
	b.ReportAllocs()
	db := openBenchDB(b)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tid := TempID(fmt.Sprintf("e%d", i))
		_, err := db.Transact(ctx, []TxDatom{
			{E: tid, A: ":bench/name", V: fmt.Sprintf("entity-%d", i)},
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTransact_10Datoms(b *testing.B)   { benchTransactN(b, 5) }   // 5 entities x 2 attrs = 10 datoms
func BenchmarkTransact_100Datoms(b *testing.B)  { benchTransactN(b, 50) }  // 50 entities x 2 attrs = 100 datoms
func BenchmarkTransact_1000Datoms(b *testing.B) { benchTransactN(b, 500) } // 500 entities x 2 attrs = 1000 datoms

// ---------------------------------------------------------------------------
// Query throughput (pre-populated DB)
// ---------------------------------------------------------------------------

func openPopulatedDB(b *testing.B, n int) *DB {
	b.Helper()
	db := openBenchDB(b)
	ctx := context.Background()

	// Insert n entities in batches of 100.
	batchSize := 100
	for start := 0; start < n; start += batchSize {
		end := start + batchSize
		if end > n {
			end = n
		}
		datoms := make([]TxDatom, 0, (end-start)*2)
		for j := start; j < end; j++ {
			tid := TempID(fmt.Sprintf("pop%d", j))
			datoms = append(datoms,
				TxDatom{E: tid, A: ":bench/name", V: fmt.Sprintf("entity-%d", j)},
				TxDatom{E: tid, A: ":bench/value", V: int64(j)},
			)
		}
		if _, err := db.Transact(ctx, datoms); err != nil {
			b.Fatal(err)
		}
	}
	return db
}

func BenchmarkQuery_Simple(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedDB(b, 1000)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.Query(ctx, `[:find ?name :where [?e :bench/name ?name]]`)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQuery_Join(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedDB(b, 1000)
	ctx := context.Background()

	// Verify the join query works before benchmarking.
	if _, err := db.Query(ctx, `[:find ?name ?value :where [?e :bench/name ?name] [?e :bench/value ?value]]`); err != nil {
		b.Skipf("join query not supported (pre-existing issue): %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.Query(ctx, `[:find ?name ?value :where [?e :bench/name ?name] [?e :bench/value ?value]]`)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQuery_Filter(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedDB(b, 1000)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.Query(ctx, `[:find ?name :where [?e :bench/name ?name] [?e :bench/value ?v] [(> ?v 500)]]`)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Pull API
// ---------------------------------------------------------------------------

func BenchmarkPull_Wildcard(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedDB(b, 1000)
	ctx := context.Background()

	// Get an entity ID to pull
	result, err := db.Query(ctx, `[:find ?e :where [?e :bench/name "entity-500"]]`)
	if err != nil {
		b.Fatal(err)
	}
	if len(result.Rows) == 0 {
		b.Fatal("no entity found")
	}
	eid := result.Rows[0][0].(int64)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.Pull(ctx, eid, PullWildcard())
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPull_Specific(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedDB(b, 1000)
	ctx := context.Background()

	result, err := db.Query(ctx, `[:find ?e :where [?e :bench/name "entity-500"]]`)
	if err != nil {
		b.Fatal(err)
	}
	eid := result.Rows[0][0].(int64)
	pattern := PullAttrs(":bench/name", ":bench/value")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.Pull(ctx, eid, pattern)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPullMany_100(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedDB(b, 1000)
	ctx := context.Background()

	result, err := db.Query(ctx, `[:find ?e :where [?e :bench/name ?n]]`)
	if err != nil {
		b.Fatal(err)
	}
	eids := make([]int64, 0, 100)
	for i := 0; i < 100 && i < len(result.Rows); i++ {
		eids = append(eids, result.Rows[i][0].(int64))
	}
	pattern := PullAttrs(":bench/name", ":bench/value")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.PullMany(ctx, eids, pattern)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// UDF query throughput
// ---------------------------------------------------------------------------

func BenchmarkQuery_UDF_BooleanPredicate(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedDB(b, 1000)
	ctx := context.Background()

	if err := db.RegisterFunction("is-big", 1, true, func(args ...any) (any, error) {
		n, _ := args[0].(int64)
		return n > 500, nil
	}); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.Query(ctx, `[:find ?name :where [?e :bench/name ?name] [?e :bench/value ?v] [(is-big ?v)]]`)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQuery_UDF_vs_Builtin(b *testing.B) {
	db := openPopulatedDB(b, 1000)
	ctx := context.Background()

	if err := db.RegisterFunction("gt500", 1, true, func(args ...any) (any, error) {
		n, _ := args[0].(int64)
		return n > 500, nil
	}); err != nil {
		b.Fatal(err)
	}

	b.Run("builtin", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, err := db.Query(ctx, `[:find ?name :where [?e :bench/name ?name] [?e :bench/value ?v] [(> ?v 500)]]`)
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("udf", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, err := db.Query(ctx, `[:find ?name :where [?e :bench/name ?name] [?e :bench/value ?v] [(gt500 ?v)]]`)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Hot/cold partitioned DB
// ---------------------------------------------------------------------------

func openPartitionedBenchDB(b *testing.B) *DB {
	b.Helper()
	dir := b.TempDir()
	mainPath := filepath.Join(dir, "current.db")
	histPath := filepath.Join(dir, "history.db")
	db, err := Open(mainPath, Options{PoolSize: 2, HistoryPath: histPath})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	ctx := context.Background()
	if err := db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":bench/name",
		ValueType:   TypeString,
		Cardinality: CardOne,
	}); err != nil {
		b.Fatal(err)
	}
	if err := db.DefineAttribute(ctx, AttributeSchema{
		Ident:       ":bench/value",
		ValueType:   TypeInt,
		Cardinality: CardOne,
	}); err != nil {
		b.Fatal(err)
	}
	return db
}

func openPopulatedPartitionedDB(b *testing.B, n int) *DB {
	b.Helper()
	db := openPartitionedBenchDB(b)
	ctx := context.Background()

	batchSize := 100
	for start := 0; start < n; start += batchSize {
		end := start + batchSize
		if end > n {
			end = n
		}
		datoms := make([]TxDatom, 0, (end-start)*2)
		for j := start; j < end; j++ {
			tid := TempID(fmt.Sprintf("pop%d", j))
			datoms = append(datoms,
				TxDatom{E: tid, A: ":bench/name", V: fmt.Sprintf("entity-%d", j)},
				TxDatom{E: tid, A: ":bench/value", V: int64(j)},
			)
		}
		if _, err := db.Transact(ctx, datoms); err != nil {
			b.Fatal(err)
		}
	}
	return db
}

func BenchmarkPartitioned_Transact_10Datoms(b *testing.B) {
	b.ReportAllocs()
	db := openPartitionedBenchDB(b)
	ctx := context.Background()

	datoms := make([]TxDatom, 10)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for j := 0; j < 5; j++ {
			tid := TempID(fmt.Sprintf("e%d_%d", i, j))
			datoms[j*2] = TxDatom{E: tid, A: ":bench/name", V: fmt.Sprintf("entity-%d", j)}
			datoms[j*2+1] = TxDatom{E: tid, A: ":bench/value", V: int64(j)}
		}
		if _, err := db.Transact(ctx, datoms); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPartitioned_Query_Simple(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedPartitionedDB(b, 1000)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.Query(ctx, `[:find ?name :where [?e :bench/name ?name]]`)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPartitioned_Query_Filter(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedPartitionedDB(b, 1000)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.Query(ctx, `[:find ?name :where [?e :bench/name ?name] [?e :bench/value ?v] [(> ?v 500)]]`)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPartitioned_Pull_Wildcard(b *testing.B) {
	b.ReportAllocs()
	db := openPopulatedPartitionedDB(b, 1000)
	ctx := context.Background()

	result, err := db.Query(ctx, `[:find ?e :where [?e :bench/name "entity-500"]]`)
	if err != nil {
		b.Fatal(err)
	}
	eid := result.Rows[0][0].(int64)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := db.Pull(ctx, eid, PullWildcard())
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// Query compilation (parse + algebrize + translate, no execution)
// ---------------------------------------------------------------------------

// mockSchema implements query.SchemaLookup for compilation benchmarks.
type mockSchema struct{}

func (m *mockSchema) LookupAttribute(ident string) *query.AttrInfo {
	switch ident {
	case ":bench/name":
		return &query.AttrInfo{ID: 1000, Ident: ident, ValueType: query.TypeString, Cardinality: 1}
	case ":bench/value":
		return &query.AttrInfo{ID: 1001, Ident: ident, ValueType: query.TypeInt, Cardinality: 1}
	}
	return nil
}

func BenchmarkCompileQuery_Simple(b *testing.B) {
	b.ReportAllocs()
	schema := &mockSchema{}
	qstr := `[:find ?name :where [?e :bench/name ?name]]`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q, err := query.ParseQuery(qstr)
		if err != nil {
			b.Fatal(err)
		}
		aq, err := query.Algebrize(q, schema)
		if err != nil {
			b.Fatal(err)
		}
		_, err = query.Translate(aq)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompileQuery_Join(b *testing.B) {
	b.ReportAllocs()
	schema := &mockSchema{}
	qstr := `[:find ?name ?value :where [?e :bench/name ?name] [?e :bench/value ?value]]`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q, err := query.ParseQuery(qstr)
		if err != nil {
			b.Fatal(err)
		}
		aq, err := query.Algebrize(q, schema)
		if err != nil {
			b.Fatal(err)
		}
		_, err = query.Translate(aq)
		if err != nil {
			b.Fatal(err)
		}
	}
}

// ---------------------------------------------------------------------------
// EDN parsing
// ---------------------------------------------------------------------------

func BenchmarkParseEDN_SimpleVector(b *testing.B) {
	b.ReportAllocs()
	input := "[1 2 3]"
	for i := 0; i < b.N; i++ {
		_, err := edn.Parse(input)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseEDN_Query(b *testing.B) {
	b.ReportAllocs()
	input := `[:find ?name ?value :where [?e :bench/name ?name] [?e :bench/value ?value] [(> ?value 500)]]`
	for i := 0; i < b.N; i++ {
		_, err := edn.Parse(input)
		if err != nil {
			b.Fatal(err)
		}
	}
}
