package query

// Query is the parsed representation of a Datalog query.
type Query struct {
	Find    []FindElem
	Where   []Clause
	In      []InputSpec
	OrderBy []OrderClause
}

// FindElem is a single element in a :find clause.
type FindElem struct {
	Variable  string // e.g. "?name"
	Aggregate string // e.g. "count", "sum" (empty if plain variable)
	AggArg    string // variable arg to aggregate
}

// Clause is a single :where pattern or expression.
type Clause interface {
	clauseMarker()
}

// Pattern is [?e :attr ?v] — a data pattern matching datoms.
type Pattern struct {
	E  PatternElem // entity
	A  PatternElem // attribute
	V  PatternElem // value
	Tx PatternElem // transaction (optional)
}

func (Pattern) clauseMarker() {}

// PatternElem is a single element within a pattern.
type PatternElem struct {
	Variable string // "?x" — non-empty means this is a variable
	Constant any    // nil if variable; string/int64/keyword if constant
	Blank    bool   // _ placeholder
}

// ExprClause is [(fn ?args...) ?result].
type ExprClause struct {
	Fn      string
	Args    []PatternElem
	Binding PatternElem
}

func (ExprClause) clauseMarker() {}

// NotClause wraps clauses in (not ...).
type NotClause struct {
	Clauses []Clause
}

func (NotClause) clauseMarker() {}

// OrClause wraps alternatives in (or ...).
type OrClause struct {
	Branches [][]Clause
}

func (OrClause) clauseMarker() {}

// RuleApplication is applying a named rule.
type RuleApplication struct {
	Name string
	Args []PatternElem
}

func (RuleApplication) clauseMarker() {}

// InputSpec represents an :in parameter.
type InputSpec struct {
	Variable string // e.g. "?name"
	Source   bool   // $ prefix = database source
}

// OrderClause for :order-by.
type OrderClause struct {
	Variable string
	Desc     bool
}

// Rule defines a Datalog rule (for recursive queries).
type Rule struct {
	Name string
	Head []PatternElem // rule head args
	Body []Clause      // rule body clauses
}
