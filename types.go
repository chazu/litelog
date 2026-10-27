package litelog

import "time"

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
	Identity    bool // unique identity: TempIDs upsert onto the entity holding the value
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
	E       any    // int64 entity ID, TempID, or LookupRef
	A       string // attribute ident e.g. ":person/name"
	V       any    // value
	Retract bool   // true = retract this fact
}

// TempID is a placeholder entity ID resolved during transaction.
type TempID string

// LookupRef identifies an existing entity by the value of an Identity
// attribute, e.g. LookupRef{A: ":user/email", V: "a@example.com"}.
type LookupRef struct {
	A string // Identity attribute ident
	V any    // value held by the entity
}

// TransactOpts configures a transaction.
type TransactOpts struct {
	// TxTime overrides the recorded transaction time; zero means now.
	// Intended for replaying transactions recorded elsewhere. Tx IDs still
	// increase monotonically, so replaying out of order yields tx_time values
	// that are not monotonic in tx ID.
	TxTime time.Time
	// ValidTime sets valid_from; zero means TxTime.
	ValidTime time.Time
}

// TxResult contains the outcome of a transaction.
type TxResult struct {
	TxID    int64            // the transaction entity ID
	TxTime  int64            // Unix milliseconds
	Tempids map[TempID]int64 // resolved tempid mappings
	Datoms  []Datom          // all datoms asserted/retracted
}
