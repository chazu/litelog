package query

import "fmt"

// ValueType constants mirrored from the litelog root package.
// The query package cannot import litelog (circular dependency).
const (
	TypeInt    = 1 // int64
	TypeString = 2 // string
	TypeFloat  = 3 // float64
	TypeBool   = 4 // bool
	TypeRef    = 5 // entity reference (int64)
	TypeBytes  = 6 // []byte
	TypeInst   = 7 // time.Time (stored as Unix ms)
)

// TypedColumn returns the type-specialized column name for a value type,
// or empty string if the type uses only the BLOB v column.
func TypedColumn(vtype int) string {
	switch vtype {
	case TypeInt, TypeRef, TypeBool, TypeInst:
		return "v_int"
	case TypeString:
		return "v_text"
	case TypeFloat:
		return "v_float"
	default:
		return ""
	}
}

// SchemaLookup provides attribute metadata to the algebrizer.
// The root litelog package implements this via SchemaCache.
type SchemaLookup interface {
	LookupAttribute(ident string) *AttrInfo
}

// AttrInfo is the attribute metadata needed by the algebrizer.
type AttrInfo struct {
	ID          int64
	Ident       string
	ValueType   int // matches litelog.ValueType constants
	Cardinality int // 1=one, 2=many
	Indexed     bool
	Unique      bool
}

// AlgebraicQuery is the output of the algebrizer — ready for SQL translation.
type AlgebraicQuery struct {
	Find          []FindMapping
	Tables        []TableBinding
	Joins         []JoinCondition
	Filters       []Filter
	RawFilters    []RawFilter // pre-built SQL fragments (e.g., temporal NOT EXISTS)
	NotClauses    []AlgebraicNot
	OrClauses     []AlgebraicOr
	OrderBy       []OrderMapping
	HasAggregates bool
}

// RawFilter is a pre-built SQL WHERE fragment with its own parameters.
// Used for complex conditions that don't fit the Filter abstraction
// (e.g., correlated NOT EXISTS subqueries for temporal queries).
type RawFilter struct {
	SQL    string
	Params []any
}

// FindMapping maps a :find element to its source table and column.
type FindMapping struct {
	Variable  string // e.g. "?name"
	Table     int    // index into Tables
	Column    string // "e", "a", "v", or "tx"
	ValueType int    // inferred type for decoding
	Aggregate string // empty, "count", "sum", etc.
	AggArg    string // variable for aggregate
}

// TableBinding represents one datom pattern → one table alias in the SQL FROM.
type TableBinding struct {
	Alias     string  // "d0", "d1", etc.
	Table     string  // "current_datoms" or "datoms" (for as-of queries)
	AttrID    int64   // resolved attribute ID (0 if unbound)
	AttrIdent string  // original ident for debugging
	ValueType int     // resolved value type (0 if unknown)
	EBound    any     // concrete entity ID if bound, nil if variable
	VBound    any     // concrete value if bound, nil if variable
	TxBound   any     // concrete tx ID if bound, nil if variable
	Pattern   Pattern // original pattern
	IndexHint string  // "eavt", "aevt", "avet", or ""
}

// JoinCondition is an equi-join between two table columns.
type JoinCondition struct {
	Variable   string // the shared variable name
	LeftTable  int    // index into Tables
	LeftCol    string // "e", "a", "v", or "tx"
	RightTable int
	RightCol   string
}

// Filter is a predicate from an ExprClause (e.g., > ?age 21).
type Filter struct {
	Fn      string      // ">", "<", "=", "!=", ">=", "<="
	Args    []FilterArg // operands
	Binding *FilterArg  // output binding (if any)
}

// FilterArg is either a variable reference or a constant.
type FilterArg struct {
	Variable  string // non-empty = reference to a variable
	Table     int    // resolved table index (for variable refs)
	Column    string // resolved column
	Constant  any    // non-nil = literal value
	ValueType int    // type for encoding constants
}

// AlgebraicNot represents a NOT EXISTS subquery.
type AlgebraicNot struct {
	Inner *AlgebraicQuery
}

// AlgebraicOr represents a UNION of alternatives.
type AlgebraicOr struct {
	Branches []*AlgebraicQuery
}

// OrderMapping maps an :order-by element to its resolved source.
type OrderMapping struct {
	Variable string
	Table    int
	Column   string
	Desc     bool
}

// varLocation records where a variable appears: which table index and column.
type varLocation struct {
	table  int
	column string
}

// algebrizer holds state during algebrization.
type algebrizer struct {
	schema   SchemaLookup
	tables   []TableBinding
	varMap   map[string][]varLocation // variable → list of appearances
	varTypes map[string]int           // variable → inferred value type
}

// Algebrize converts a parsed Query into an AlgebraicQuery using schema information.
func Algebrize(q *Query, schema SchemaLookup) (*AlgebraicQuery, error) {
	alg := &algebrizer{
		schema:   schema,
		varMap:   make(map[string][]varLocation),
		varTypes: make(map[string]int),
	}

	// Pre-register :in variables so they can be referenced in filters
	// even if they don't appear in any datom pattern. They get a sentinel
	// location (table=-1) that marks them as external inputs.
	for _, inp := range q.In {
		if !inp.Source && inp.Variable != "" {
			alg.varMap[inp.Variable] = []varLocation{{table: -1, column: "input"}}
		}
	}

	aq := &AlgebraicQuery{}

	// Phase 1: Walk :where clauses, build table bindings.
	var notClauses []NotClause
	var orClauses []OrClause
	var exprClauses []ExprClause

	for _, clause := range q.Where {
		switch c := clause.(type) {
		case Pattern:
			if err := alg.addPattern(c); err != nil {
				return nil, err
			}
		case ExprClause:
			exprClauses = append(exprClauses, c)
		case NotClause:
			notClauses = append(notClauses, c)
		case OrClause:
			orClauses = append(orClauses, c)
		default:
			return nil, fmt.Errorf("algebrize: unsupported clause type %T", c)
		}
	}

	aq.Tables = alg.tables

	// Phase 2: Build join conditions from shared variables.
	aq.Joins = alg.buildJoins()

	// Phase 3: Resolve expression clauses to filters.
	for _, ec := range exprClauses {
		f, err := alg.resolveFilter(ec)
		if err != nil {
			return nil, err
		}
		aq.Filters = append(aq.Filters, f)
	}

	// Phase 4: Resolve NOT clauses.
	for _, nc := range notClauses {
		inner, err := alg.algebrizeSubquery(nc.Clauses)
		if err != nil {
			return nil, fmt.Errorf("algebrize NOT: %w", err)
		}
		// Link shared variables: join the inner subquery to the outer tables.
		inner.Joins = append(inner.Joins, alg.buildSubqueryJoins(inner)...)
		aq.NotClauses = append(aq.NotClauses, AlgebraicNot{Inner: inner})
	}

	// Phase 5: Resolve OR clauses.
	for _, oc := range orClauses {
		var branches []*AlgebraicQuery
		for _, branch := range oc.Branches {
			bq, err := alg.algebrizeSubquery(branch)
			if err != nil {
				return nil, fmt.Errorf("algebrize OR branch: %w", err)
			}
			bq.Joins = append(bq.Joins, alg.buildSubqueryJoins(bq)...)
			branches = append(branches, bq)
		}
		aq.OrClauses = append(aq.OrClauses, AlgebraicOr{Branches: branches})
	}

	// Phase 6: Map :find elements to output columns.
	for _, fe := range q.Find {
		fm, err := alg.resolveFindElem(fe)
		if err != nil {
			return nil, err
		}
		if fm.Aggregate != "" {
			aq.HasAggregates = true
		}
		aq.Find = append(aq.Find, fm)
	}

	// Phase 7: Map :order-by elements.
	for _, ob := range q.OrderBy {
		om, err := alg.resolveOrderBy(ob)
		if err != nil {
			return nil, err
		}
		aq.OrderBy = append(aq.OrderBy, om)
	}

	return aq, nil
}

// addPattern processes a single Pattern clause, creating a TableBinding and
// registering variable appearances.
func (a *algebrizer) addPattern(p Pattern) error {
	idx := len(a.tables)
	tb := TableBinding{
		Alias:   fmt.Sprintf("d%d", idx),
		Table:   "current_datoms",
		Pattern: p,
	}

	// Resolve entity element.
	if p.E.Variable != "" {
		a.recordVar(p.E.Variable, idx, "e")
	} else if p.E.Constant != nil {
		tb.EBound = p.E.Constant
	}

	// Resolve attribute element.
	if p.A.Variable != "" {
		a.recordVar(p.A.Variable, idx, "a")
	} else if p.A.Constant != nil {
		ident, ok := p.A.Constant.(string)
		if !ok {
			return fmt.Errorf("algebrize: attribute constant must be string, got %T", p.A.Constant)
		}
		tb.AttrIdent = ident
		info := a.schema.LookupAttribute(ident)
		if info == nil {
			return fmt.Errorf("algebrize: unknown attribute %q", ident)
		}
		tb.AttrID = info.ID
		tb.ValueType = info.ValueType
	}

	// Resolve value element.
	if p.V.Variable != "" {
		a.recordVar(p.V.Variable, idx, "v")
		// Infer variable type from the attribute's value type.
		if tb.ValueType != 0 {
			a.varTypes[p.V.Variable] = tb.ValueType
		}
	} else if p.V.Constant != nil {
		tb.VBound = p.V.Constant
	}

	// Resolve tx element.
	if p.Tx.Variable != "" {
		a.recordVar(p.Tx.Variable, idx, "tx")
	} else if p.Tx.Constant != nil {
		tb.TxBound = p.Tx.Constant
	}

	// Determine index hint.
	tb.IndexHint = a.chooseIndex(tb)

	a.tables = append(a.tables, tb)
	return nil
}

// chooseIndex selects the best index hint for a table binding based on which
// elements are bound.
func (a *algebrizer) chooseIndex(tb TableBinding) string {
	eBound := tb.EBound != nil
	aBound := tb.AttrID != 0
	vBound := tb.VBound != nil

	switch {
	case eBound:
		return "eavt"
	case aBound && vBound:
		// Use AVET only if the attribute is indexed.
		info := a.schema.LookupAttribute(tb.AttrIdent)
		if info != nil && info.Indexed {
			return "avet"
		}
		return "aevt"
	case aBound:
		return "aevt"
	default:
		return ""
	}
}

// recordVar registers that a variable appears at (table, column).
func (a *algebrizer) recordVar(variable string, table int, column string) {
	a.varMap[variable] = append(a.varMap[variable], varLocation{table: table, column: column})
}

// buildJoins creates equi-join conditions for variables that appear in 2+ patterns.
func (a *algebrizer) buildJoins() []JoinCondition {
	var joins []JoinCondition
	for v, locs := range a.varMap {
		for i := 1; i < len(locs); i++ {
			joins = append(joins, JoinCondition{
				Variable:   v,
				LeftTable:  locs[0].table,
				LeftCol:    locs[0].column,
				RightTable: locs[i].table,
				RightCol:   locs[i].column,
			})
		}
	}
	return joins
}

// resolveFilter converts an ExprClause into a Filter with resolved variable references.
func (a *algebrizer) resolveFilter(ec ExprClause) (Filter, error) {
	f := Filter{Fn: ec.Fn}

	for _, arg := range ec.Args {
		fa, err := a.resolveFilterArg(arg)
		if err != nil {
			return f, fmt.Errorf("algebrize filter %q: %w", ec.Fn, err)
		}
		f.Args = append(f.Args, fa)
	}

	// Propagate value types from variable args to constant args so that
	// the executor can BLOB-encode constants for comparison against v columns.
	var inferredType int
	for _, fa := range f.Args {
		if fa.Variable != "" && fa.ValueType != 0 {
			inferredType = fa.ValueType
			break
		}
	}
	if inferredType != 0 {
		for i := range f.Args {
			if f.Args[i].Variable == "" && f.Args[i].ValueType == 0 {
				f.Args[i].ValueType = inferredType
			}
		}
	}

	if ec.Binding.Variable != "" || ec.Binding.Constant != nil {
		fa, err := a.resolveFilterArg(ec.Binding)
		if err != nil {
			return f, fmt.Errorf("algebrize filter %q binding: %w", ec.Fn, err)
		}
		f.Binding = &fa
	}

	return f, nil
}

// resolveFilterArg resolves a PatternElem to a FilterArg.
func (a *algebrizer) resolveFilterArg(pe PatternElem) (FilterArg, error) {
	if pe.Variable != "" {
		locs, ok := a.varMap[pe.Variable]
		if !ok || len(locs) == 0 {
			return FilterArg{}, fmt.Errorf("unresolved variable %s in filter", pe.Variable)
		}
		loc := locs[0]
		if loc.table == -1 {
			// This is an :in input variable. Return it as a variable ref
			// with a sentinel table index; bindInputArgs will replace it
			// with a constant before SQL execution.
			return FilterArg{
				Variable:  pe.Variable,
				Table:     -1,
				Column:    "input",
				ValueType: a.varTypes[pe.Variable],
			}, nil
		}
		return FilterArg{
			Variable:  pe.Variable,
			Table:     loc.table,
			Column:    loc.column,
			ValueType: a.varTypes[pe.Variable],
		}, nil
	}
	// Constant.
	return FilterArg{
		Constant: pe.Constant,
	}, nil
}

// resolveFindElem maps a FindElem to its source table and column.
func (a *algebrizer) resolveFindElem(fe FindElem) (FindMapping, error) {
	// For aggregates with no inner variable (e.g., count), use the aggregate arg.
	lookupVar := fe.Variable
	if lookupVar == "" {
		lookupVar = fe.AggArg
	}

	locs, ok := a.varMap[lookupVar]
	if !ok || len(locs) == 0 {
		return FindMapping{}, fmt.Errorf("algebrize: :find variable %s not found in :where", lookupVar)
	}
	loc := locs[0]

	return FindMapping{
		Variable:  fe.Variable,
		Table:     loc.table,
		Column:    loc.column,
		ValueType: a.varTypes[lookupVar],
		Aggregate: fe.Aggregate,
		AggArg:    fe.AggArg,
	}, nil
}

// resolveOrderBy maps an OrderClause to its resolved source.
func (a *algebrizer) resolveOrderBy(ob OrderClause) (OrderMapping, error) {
	locs, ok := a.varMap[ob.Variable]
	if !ok || len(locs) == 0 {
		return OrderMapping{}, fmt.Errorf("algebrize: :order-by variable %s not found in :where", ob.Variable)
	}
	loc := locs[0]
	return OrderMapping{
		Variable: ob.Variable,
		Table:    loc.table,
		Column:   loc.column,
		Desc:     ob.Desc,
	}, nil
}

// algebrizeSubquery processes a list of clauses as a subquery (for NOT/OR).
// It creates its own set of table bindings but inherits the parent schema.
func (a *algebrizer) algebrizeSubquery(clauses []Clause) (*AlgebraicQuery, error) {
	sub := &algebrizer{
		schema:   a.schema,
		varMap:   make(map[string][]varLocation),
		varTypes: make(map[string]int),
	}

	// Copy parent type information so subquery variables can inherit types.
	for k, v := range a.varTypes {
		sub.varTypes[k] = v
	}

	aq := &AlgebraicQuery{}

	for _, clause := range clauses {
		switch c := clause.(type) {
		case Pattern:
			if err := sub.addPattern(c); err != nil {
				return nil, err
			}
		case ExprClause:
			f, err := sub.resolveFilter(c)
			if err != nil {
				return nil, err
			}
			aq.Filters = append(aq.Filters, f)
		case NotClause:
			inner, err := sub.algebrizeSubquery(c.Clauses)
			if err != nil {
				return nil, err
			}
			inner.Joins = append(inner.Joins, sub.buildSubqueryJoins(inner)...)
			aq.NotClauses = append(aq.NotClauses, AlgebraicNot{Inner: inner})
		case OrClause:
			var branches []*AlgebraicQuery
			for _, branch := range c.Branches {
				bq, err := sub.algebrizeSubquery(branch)
				if err != nil {
					return nil, err
				}
				bq.Joins = append(bq.Joins, sub.buildSubqueryJoins(bq)...)
				branches = append(branches, bq)
			}
			aq.OrClauses = append(aq.OrClauses, AlgebraicOr{Branches: branches})
		default:
			return nil, fmt.Errorf("algebrize subquery: unsupported clause type %T", c)
		}
	}

	aq.Tables = sub.tables
	aq.Joins = sub.buildJoins()

	return aq, nil
}

// buildSubqueryJoins creates join conditions linking a subquery's variables
// back to the parent query's tables. This is used for NOT EXISTS correlation
// and OR UNION correlation.
func (a *algebrizer) buildSubqueryJoins(sub *AlgebraicQuery) []JoinCondition {
	var joins []JoinCondition

	// Build a map of the subquery's variables.
	subVars := make(map[string]varLocation)
	for i, tb := range sub.Tables {
		p := tb.Pattern
		if p.E.Variable != "" {
			if _, exists := subVars[p.E.Variable]; !exists {
				subVars[p.E.Variable] = varLocation{table: i, column: "e"}
			}
		}
		if p.A.Variable != "" {
			if _, exists := subVars[p.A.Variable]; !exists {
				subVars[p.A.Variable] = varLocation{table: i, column: "a"}
			}
		}
		if p.V.Variable != "" {
			if _, exists := subVars[p.V.Variable]; !exists {
				subVars[p.V.Variable] = varLocation{table: i, column: "v"}
			}
		}
		if p.Tx.Variable != "" {
			if _, exists := subVars[p.Tx.Variable]; !exists {
				subVars[p.Tx.Variable] = varLocation{table: i, column: "tx"}
			}
		}
	}

	// For each variable that appears in both parent and subquery, create a join.
	for v, subLoc := range subVars {
		if parentLocs, ok := a.varMap[v]; ok && len(parentLocs) > 0 {
			parentLoc := parentLocs[0]
			joins = append(joins, JoinCondition{
				Variable:   v,
				LeftTable:  parentLoc.table,
				LeftCol:    parentLoc.column,
				RightTable: subLoc.table,
				RightCol:   subLoc.column,
			})
		}
	}

	return joins
}
