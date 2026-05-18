package query

import (
	"fmt"
	"strings"
)

// RuleSet is a collection of rules that may reference each other recursively.
type RuleSet struct {
	Rules []*Rule
}

// RecursivePlan is the execution plan for evaluating recursive rules.
type RecursivePlan struct {
	// SetupSQL creates temporary tables for derived relations.
	SetupSQL []string

	// BaseSQL populates initial facts from non-recursive rule bodies.
	BaseSQL []SQLQuery

	// DeltaSQL is the iterative step — joins delta with base/derived.
	// Executed in a loop until no new rows.
	DeltaSQL []SQLQuery

	// CleanupSQL drops temporary tables.
	CleanupSQL []string

	// ResultTable is the name of the temp table holding final results.
	ResultTable string

	// Columns in the result table (positional, matching rule head args).
	Columns []string
}

// PlanRecursive creates an execution plan for a set of rules.
// All rules must share the same head name and arity (single-predicate recursion),
// or be part of a mutual-recursion group.
func PlanRecursive(rules []*Rule, schema SchemaLookup) (*RecursivePlan, error) {
	if len(rules) == 0 {
		return nil, fmt.Errorf("PlanRecursive: no rules provided")
	}

	// Build the set of rule names for reference detection.
	ruleNames := make(map[string]bool)
	for _, r := range rules {
		ruleNames[r.Name] = true
	}

	// Determine arity per rule name and validate consistency.
	ruleArity := make(map[string]int)
	for _, r := range rules {
		arity := len(r.Head)
		if prev, ok := ruleArity[r.Name]; ok {
			if prev != arity {
				return nil, fmt.Errorf("PlanRecursive: rule %q has inconsistent arity (%d vs %d)", r.Name, prev, arity)
			}
		} else {
			ruleArity[r.Name] = arity
		}
	}

	// Step 1: Classify rules into non-recursive (base) and recursive.
	var baseRules, recursiveRules []*Rule
	for _, r := range rules {
		if ruleIsRecursive(r, ruleNames) {
			recursiveRules = append(recursiveRules, r)
		} else {
			baseRules = append(baseRules, r)
		}
	}

	plan := &RecursivePlan{}

	// Use the first rule's name as the primary result table.
	primaryName := rules[0].Name
	plan.ResultTable = "_rule_" + primaryName
	arity := ruleArity[primaryName]
	plan.Columns = make([]string, arity)
	for i := 0; i < arity; i++ {
		plan.Columns[i] = fmt.Sprintf("c%d", i)
	}

	// Step 2: Create temp tables for each unique rule name.
	for name, ar := range ruleArity {
		cols := make([]string, ar)
		for i := 0; i < ar; i++ {
			cols[i] = fmt.Sprintf("c%d", i)
		}
		colList := strings.Join(cols, ", ")
		plan.SetupSQL = append(plan.SetupSQL,
			fmt.Sprintf("CREATE TEMP TABLE IF NOT EXISTS _rule_%s (%s, PRIMARY KEY(%s)) WITHOUT ROWID", name, colList, colList),
			fmt.Sprintf("CREATE TEMP TABLE IF NOT EXISTS _delta_%s (%s, PRIMARY KEY(%s)) WITHOUT ROWID", name, colList, colList),
		)
	}

	// Step 3: Generate base case SQL from non-recursive rules.
	for _, r := range baseRules {
		sq, err := ruleBodyToSQL(r, schema, ruleArity, nil)
		if err != nil {
			return nil, fmt.Errorf("PlanRecursive base rule %q: %w", r.Name, err)
		}
		plan.BaseSQL = append(plan.BaseSQL, *sq)
	}

	// Seed delta tables from base facts.
	for name := range ruleArity {
		plan.BaseSQL = append(plan.BaseSQL, SQLQuery{
			SQL: fmt.Sprintf("INSERT OR IGNORE INTO _delta_%s SELECT * FROM _rule_%s", name, name),
		})
	}

	// Step 4: Generate delta iteration SQL for recursive rules.
	// Preamble: clear delta tables before each iteration.
	for name := range ruleArity {
		plan.DeltaSQL = append(plan.DeltaSQL, SQLQuery{
			SQL: fmt.Sprintf("DELETE FROM _delta_%s", name),
		})
	}

	for _, r := range recursiveRules {
		// Find which rule names are referenced in the body.
		referencedRules := bodyRuleRefs(r, ruleNames)

		// For each referenced rule, produce a variant where that reference
		// uses the delta table. Semi-naive: join new (delta) facts with
		// existing (derived) facts.
		for deltaName := range referencedRules {
			useDelta := map[string]bool{deltaName: true}
			sq, err := ruleBodyToSQL(r, schema, ruleArity, useDelta)
			if err != nil {
				return nil, fmt.Errorf("PlanRecursive delta rule %q (delta %s): %w", r.Name, deltaName, err)
			}
			// INSERT OR IGNORE handles dedup via the PRIMARY KEY constraint.
			plan.DeltaSQL = append(plan.DeltaSQL, *sq)
		}
	}

	// Step 5: Cleanup SQL.
	for name := range ruleArity {
		plan.CleanupSQL = append(plan.CleanupSQL,
			fmt.Sprintf("DROP TABLE IF EXISTS _rule_%s", name),
			fmt.Sprintf("DROP TABLE IF EXISTS _delta_%s", name),
		)
	}

	return plan, nil
}

// ruleIsRecursive checks whether a rule's body references any rule in the set.
func ruleIsRecursive(r *Rule, ruleNames map[string]bool) bool {
	for _, clause := range r.Body {
		if ra, ok := clause.(RuleApplication); ok {
			if ruleNames[ra.Name] {
				return true
			}
		}
	}
	return false
}

// bodyRuleRefs returns the set of rule names referenced in a rule's body.
func bodyRuleRefs(r *Rule, ruleNames map[string]bool) map[string]bool {
	refs := make(map[string]bool)
	for _, clause := range r.Body {
		if ra, ok := clause.(RuleApplication); ok {
			if ruleNames[ra.Name] {
				refs[ra.Name] = true
			}
		}
	}
	return refs
}

// varTracker collects variable bindings and generates equi-join WHERE conditions.
type varTracker struct {
	firstRef map[string]string // variable -> first column reference
	joins    []string          // equi-join conditions
}

func newVarTracker() *varTracker {
	return &varTracker{
		firstRef: make(map[string]string),
	}
}

// record registers a variable at a column reference. If the variable was
// already seen, an equi-join condition is added.
func (vt *varTracker) record(variable, colRef string) {
	if prev, exists := vt.firstRef[variable]; exists {
		vt.joins = append(vt.joins, prev+" = "+colRef)
	} else {
		vt.firstRef[variable] = colRef
	}
}

// resolve returns the first column reference for a variable.
func (vt *varTracker) resolve(variable string) (string, bool) {
	ref, ok := vt.firstRef[variable]
	return ref, ok
}

// ruleBodyToSQL translates a rule's body into an INSERT OR IGNORE statement
// that populates the rule's derived table.
//
// useDelta: if non-nil, maps rule names that should be read from _delta_{name}
// instead of _rule_{name}. This implements the semi-naive "only join new facts" step.
func ruleBodyToSQL(r *Rule, schema SchemaLookup, ruleArity map[string]int, useDelta map[string]bool) (*SQLQuery, error) {
	var (
		fromParts []string
		params    []any
		aliasIdx  int
	)

	vt := newVarTracker()
	var whereParts []string

	// Process each body clause.
	for _, clause := range r.Body {
		switch c := clause.(type) {
		case Pattern:
			alias := fmt.Sprintf("d%d", aliasIdx)
			aliasIdx++

			fromParts = append(fromParts, "current_datoms AS "+alias)

			// Bind attribute.
			if c.A.Constant != nil {
				ident, ok := c.A.Constant.(string)
				if !ok {
					return nil, fmt.Errorf("rule body: attribute constant must be string, got %T", c.A.Constant)
				}
				info := schema.LookupAttribute(ident)
				if info == nil {
					return nil, fmt.Errorf("rule body: unknown attribute %q", ident)
				}
				whereParts = append(whereParts, alias+".a = ?")
				params = append(params, info.ID)
			} else if c.A.Variable != "" {
				vt.record(c.A.Variable, alias+".a")
			}

			// Bind entity.
			if c.E.Variable != "" {
				vt.record(c.E.Variable, alias+".e")
			} else if c.E.Constant != nil {
				whereParts = append(whereParts, alias+".e = ?")
				params = append(params, c.E.Constant)
			}

			// Bind value.
			if c.V.Variable != "" {
				vt.record(c.V.Variable, alias+".v")
			} else if c.V.Constant != nil {
				whereParts = append(whereParts, alias+".v = ?")
				params = append(params, c.V.Constant)
			}

			// Bind tx.
			if c.Tx.Variable != "" {
				vt.record(c.Tx.Variable, alias+".tx")
			} else if c.Tx.Constant != nil {
				whereParts = append(whereParts, alias+".tx = ?")
				params = append(params, c.Tx.Constant)
			}

		case RuleApplication:
			alias := fmt.Sprintf("r%d", aliasIdx)
			aliasIdx++

			arity, ok := ruleArity[c.Name]
			if !ok {
				return nil, fmt.Errorf("rule body: unknown rule %q", c.Name)
			}
			if len(c.Args) != arity {
				return nil, fmt.Errorf("rule body: rule %q expects %d args, got %d", c.Name, arity, len(c.Args))
			}

			// Choose _rule_ or _delta_ table.
			tableName := "_rule_" + c.Name
			if useDelta != nil && useDelta[c.Name] {
				tableName = "_delta_" + c.Name
			}
			fromParts = append(fromParts, tableName+" AS "+alias)

			// Bind each argument.
			for i, arg := range c.Args {
				col := fmt.Sprintf("%s.c%d", alias, i)
				if arg.Variable != "" {
					vt.record(arg.Variable, col)
				} else if arg.Constant != nil {
					whereParts = append(whereParts, col+" = ?")
					params = append(params, arg.Constant)
				}
			}

		case ExprClause:
			frag, p, err := exprClauseToSQL(c, vt.firstRef)
			if err != nil {
				return nil, fmt.Errorf("rule body expr: %w", err)
			}
			whereParts = append(whereParts, frag)
			params = append(params, p...)

		default:
			return nil, fmt.Errorf("rule body: unsupported clause type %T", clause)
		}
	}

	// Append equi-join conditions from the variable tracker.
	whereParts = append(whereParts, vt.joins...)

	// Build SELECT columns from head variables.
	selectCols := make([]string, len(r.Head))
	for i, elem := range r.Head {
		if elem.Variable != "" {
			ref, ok := vt.resolve(elem.Variable)
			if !ok {
				return nil, fmt.Errorf("rule body: head variable %s not bound in body", elem.Variable)
			}
			selectCols[i] = ref
		} else if elem.Constant != nil {
			selectCols[i] = "?"
			params = append(params, elem.Constant)
		} else {
			return nil, fmt.Errorf("rule body: head element %d is neither variable nor constant", i)
		}
	}

	// Build target column list.
	targetCols := make([]string, len(r.Head))
	for i := range r.Head {
		targetCols[i] = fmt.Sprintf("c%d", i)
	}

	sql := fmt.Sprintf("INSERT OR IGNORE INTO _rule_%s (%s)\nSELECT DISTINCT %s",
		r.Name,
		strings.Join(targetCols, ", "),
		strings.Join(selectCols, ", "),
	)

	if len(fromParts) > 0 {
		sql += "\nFROM " + strings.Join(fromParts, ", ")
	}

	allWhere := whereParts
	if len(allWhere) > 0 {
		sql += "\nWHERE " + strings.Join(allWhere, " AND ")
	}

	return &SQLQuery{SQL: sql, Params: params}, nil
}

// exprClauseToSQL translates an ExprClause into a SQL WHERE fragment.
func exprClauseToSQL(ec ExprClause, varMap map[string]string) (string, []any, error) {
	op, ok := filterOps[ec.Fn]
	if !ok {
		op = ec.Fn
	}

	var params []any
	refs := make([]string, len(ec.Args))
	for i, arg := range ec.Args {
		if arg.Variable != "" {
			ref, exists := varMap[arg.Variable]
			if !exists {
				return "", nil, fmt.Errorf("unresolved variable %s in expression", arg.Variable)
			}
			refs[i] = ref
		} else {
			refs[i] = "?"
			params = append(params, arg.Constant)
		}
	}

	if len(refs) == 2 {
		return refs[0] + " " + op + " " + refs[1], params, nil
	}
	return strings.Join(refs, " "+op+" "), params, nil
}

// ParseRulesAndQuery parses a combined rules + query input.
// The rulesInput should be a vector of rule vectors.
// The queryInput should be a standard Datalog query vector.
func ParseRulesAndQuery(rulesInput, queryInput string) ([]*Rule, *Query, error) {
	rules, err := ParseRules(rulesInput)
	if err != nil {
		return nil, nil, fmt.Errorf("parse rules: %w", err)
	}

	query, err := ParseQuery(queryInput)
	if err != nil {
		return nil, nil, fmt.Errorf("parse query: %w", err)
	}

	return rules, query, nil
}
