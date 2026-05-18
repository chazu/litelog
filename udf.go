package litelog

import (
	"fmt"
	"sync"

	"zombiezen.com/go/sqlite"
)

// UDFFunc is a user-defined function callable from Datalog queries.
// Arguments are decoded Go values. Return value is passed back to SQLite.
// For boolean predicates, return true/false. For value functions, return
// the computed value.
type UDFFunc func(args ...any) (any, error)

// UDFDef describes a registered user-defined function.
type UDFDef struct {
	Name          string
	NArgs         int // number of arguments (-1 for variadic)
	Deterministic bool
	Fn            UDFFunc
}

// udfRegistry manages registered user-defined functions.
type udfRegistry struct {
	mu   sync.RWMutex
	udfs map[string]*UDFDef
}

func newUDFRegistry() *udfRegistry {
	return &udfRegistry{udfs: make(map[string]*UDFDef)}
}

func (r *udfRegistry) register(def *UDFDef) {
	r.mu.Lock()
	r.udfs[def.Name] = def
	r.mu.Unlock()
}

func (r *udfRegistry) get(name string) *UDFDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.udfs[name]
}

func (r *udfRegistry) all() []*UDFDef {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*UDFDef, 0, len(r.udfs))
	for _, def := range r.udfs {
		result = append(result, def)
	}
	return result
}

// RegisterFunction registers a user-defined function that can be called
// from Datalog queries using [(fn-name ?arg1 ?arg2)] syntax.
//
// The function is registered on all pool connections. Existing connections
// are updated lazily on next use.
//
// For boolean predicates (used as WHERE filters), the function should return
// a bool. For value-producing functions, return the computed value.
func (db *DB) RegisterFunction(name string, nArgs int, deterministic bool, fn UDFFunc) error {
	def := &UDFDef{
		Name:          name,
		NArgs:         nArgs,
		Deterministic: deterministic,
		Fn:            fn,
	}
	db.udfs.register(def)

	// Mark that connections need UDF registration on next use.
	db.udfsDirty.Store(true)

	return nil
}

// installUDFs registers all UDFs on a connection. Called lazily when a
// connection is taken from the pool.
func (db *DB) installUDFs(conn *sqlite.Conn) error {
	for _, def := range db.udfs.all() {
		if err := installOneUDF(conn, def); err != nil {
			return err
		}
	}
	return nil
}

func installOneUDF(conn *sqlite.Conn, def *UDFDef) error {
	fn := def.Fn
	return conn.CreateFunction(def.Name, &sqlite.FunctionImpl{
		NArgs:         def.NArgs,
		Deterministic: def.Deterministic,
		Scalar: func(ctx sqlite.Context, args []sqlite.Value) (sqlite.Value, error) {
			goArgs := make([]any, len(args))
			for i, arg := range args {
				switch arg.Type() {
				case sqlite.TypeInteger:
					goArgs[i] = arg.Int64()
				case sqlite.TypeFloat:
					goArgs[i] = arg.Float()
				case sqlite.TypeText:
					goArgs[i] = arg.Text()
				case sqlite.TypeBlob:
					goArgs[i] = arg.Blob()
				case sqlite.TypeNull:
					goArgs[i] = nil
				}
			}
			result, err := fn(goArgs...)
			if err != nil {
				return sqlite.Value{}, err
			}
			return goValueToSQLite(result)
		},
	})
}

func goValueToSQLite(v any) (sqlite.Value, error) {
	switch val := v.(type) {
	case nil:
		return sqlite.Value{}, nil
	case bool:
		if val {
			return sqlite.IntegerValue(1), nil
		}
		return sqlite.IntegerValue(0), nil
	case int64:
		return sqlite.IntegerValue(val), nil
	case int:
		return sqlite.IntegerValue(int64(val)), nil
	case float64:
		return sqlite.FloatValue(val), nil
	case string:
		return sqlite.TextValue(val), nil
	case []byte:
		return sqlite.BlobValue(val), nil
	default:
		return sqlite.Value{}, fmt.Errorf("litelog: UDF returned unsupported type %T", v)
	}
}
