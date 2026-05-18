package litelog

// ValueType represents the type of a datom's value.
type ValueType int

const (
	TypeInt    ValueType = iota + 1 // int64
	TypeString                      // string
	TypeFloat                       // float64
	TypeBool                        // bool
	TypeRef                         // entity reference (int64)
	TypeBytes                       // []byte
	TypeInst                        // time.Time (stored as Unix ms)
)

// Cardinality of an attribute.
type Cardinality int

const (
	CardOne  Cardinality = 1 // single value
	CardMany Cardinality = 2 // set of values
)

// AttributeSchema defines an attribute's metadata.
type AttributeSchema struct {
	Ident       string      // e.g. ":person/name"
	ValueType   ValueType
	Cardinality Cardinality
	Indexed     bool // create AVET index for this attr
	Unique      bool // enforce uniqueness on value
	IsComponent bool // component entity (cascade delete)
}

// Datom is a single fact.
type Datom struct {
	E     int64 // entity ID
	A     int64 // attribute ID (interned)
	V     any   // value (typed by attribute schema)
	Tx    int64 // transaction ID
	Added bool  // true=assert, false=retract
}

// TxDatom is input to a transaction — uses string idents, not interned IDs.
type TxDatom struct {
	E       any    // int64 entity ID or TempID
	A       string // attribute ident e.g. ":person/name"
	V       any    // value
	Retract bool   // true = retract this fact
}

// TempID is a placeholder entity ID resolved during transaction.
type TempID string

// TxResult contains the outcome of a transaction.
type TxResult struct {
	TxID    int64            // the transaction entity ID
	TxTime  int64            // Unix milliseconds
	Tempids map[TempID]int64 // resolved tempid mappings
	Datoms  []Datom          // all datoms asserted/retracted
}
