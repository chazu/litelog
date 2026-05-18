package query

import (
	"fmt"
	"strings"
)

// SQLQuery is the output of the translator — ready for execution.
type SQLQuery struct {
	SQL    string
	Params []any // positional parameters for ? placeholders
}

// Translate converts an AlgebraicQuery into executable SQL text with
// positional parameter bindings.
//
// For filter constants, values are added to Params as-is; the executor
// layer is responsible for encoding them (via values.go) before execution.
// Because the order-preserving BLOB encoding in values.go makes raw BLOB
// comparison semantically correct for ints and floats, the generated SQL
// uses plain comparison operators on the v column.
func Translate(aq *AlgebraicQuery) (*SQLQuery, error) {
	if len(aq.Tables) == 0 {
		return nil, fmt.Errorf("translate: no tables in algebraic query")
	}

	// If there are OR clauses, we handle them by translating each branch
	// into a full query and combining with UNION ALL.
	if len(aq.OrClauses) > 0 {
		return translateWithOr(aq)
	}

	return translateCore(aq)
}

// translateCore handles the common case: no OR clauses.
func translateCore(aq *AlgebraicQuery) (*SQLQuery, error) {
	var (
		params []any
		parts  []string
	)

	// ---- SELECT ----
	parts = append(parts, buildSelect(aq))

	// ---- FROM ----
	parts = append(parts, buildFrom(aq))

	// ---- WHERE ----
	whereFrags, whereParams := buildWhere(aq)
	params = append(params, whereParams...)

	// NOT EXISTS sub-queries.
	for i := range aq.NotClauses {
		frag, p, err := buildNotExists(aq, &aq.NotClauses[i])
		if err != nil {
			return nil, err
		}
		whereFrags = append(whereFrags, frag)
		params = append(params, p...)
	}

	if len(whereFrags) > 0 {
		parts = append(parts, "WHERE "+strings.Join(whereFrags, " AND "))
	}

	// ---- GROUP BY ----
	if aq.HasAggregates {
		groupCols := buildGroupBy(aq)
		if len(groupCols) > 0 {
			parts = append(parts, "GROUP BY "+strings.Join(groupCols, ", "))
		}
	}

	// ---- ORDER BY ----
	if len(aq.OrderBy) > 0 {
		if clause := buildOrderBy(aq); clause != "" {
			parts = append(parts, clause)
		}
	}

	return &SQLQuery{
		SQL:    strings.Join(parts, "\n"),
		Params: params,
	}, nil
}

// ---------------------------------------------------------------------------
// SELECT
// ---------------------------------------------------------------------------

func buildSelect(aq *AlgebraicQuery) string {
	var cols []string
	for _, fm := range aq.Find {
		alias := aq.Tables[fm.Table].Alias
		if fm.Aggregate != "" {
			aggFn := strings.ToUpper(fm.Aggregate)
			argRef := alias + "." + fm.Column
			if fm.Column == "v" && fm.ValueType != 0 {
				if tc := TypedColumn(fm.ValueType); tc != "" {
					argRef = alias + "." + tc
				}
			}
			cols = append(cols, fmt.Sprintf("%s(%s)", aggFn, argRef))
		} else {
			if fm.Column == "v" && fm.ValueType != 0 {
				if tc := TypedColumn(fm.ValueType); tc != "" {
					cols = append(cols, alias+"."+tc)
					cols = append(cols, alias+".v_type")
					continue
				}
			}
			cols = append(cols, alias+"."+fm.Column)
			if fm.Column == "v" {
				cols = append(cols, alias+".v_type")
			}
		}
	}

	distinct := ""
	if !aq.HasAggregates {
		distinct = "DISTINCT "
	}

	return fmt.Sprintf("SELECT %s%s", distinct, strings.Join(cols, ", "))
}

// ---------------------------------------------------------------------------
// FROM
// ---------------------------------------------------------------------------

func buildFrom(aq *AlgebraicQuery) string {
	var tables []string
	for _, tb := range aq.Tables {
		entry := tb.Table + " AS " + tb.Alias
		if tb.IndexHint != "" {
			entry += " INDEXED BY " + tb.IndexHint
		}
		tables = append(tables, entry)
	}
	return "FROM " + strings.Join(tables, ", ")
}

// ---------------------------------------------------------------------------
// WHERE
// ---------------------------------------------------------------------------

func buildWhere(aq *AlgebraicQuery) (frags []string, params []any) {
	// 1. Table-level constant bindings.
	for _, tb := range aq.Tables {
		if tb.AttrID != 0 {
			frags = append(frags, tb.Alias+".a = ?")
			params = append(params, tb.AttrID)
		}
		if tb.EBound != nil {
			frags = append(frags, tb.Alias+".e = ?")
			params = append(params, tb.EBound)
		}
		if tb.VBound != nil {
			if tc := TypedColumn(tb.ValueType); tc != "" {
				frags = append(frags, tb.Alias+"."+tc+" = ?")
			} else {
				frags = append(frags, tb.Alias+".v = ?")
			}
			params = append(params, tb.VBound)
		}
		if tb.TxBound != nil {
			frags = append(frags, tb.Alias+".tx = ?")
			params = append(params, tb.TxBound)
		}
	}

	// 2. Join conditions (implicit equi-joins between tables).
	for _, jc := range aq.Joins {
		left := aq.Tables[jc.LeftTable].Alias + "." + jc.LeftCol
		right := aq.Tables[jc.RightTable].Alias + "." + jc.RightCol
		frags = append(frags, left+" = "+right)
	}

	// 3. Filters (predicate expressions).
	for _, f := range aq.Filters {
		frag, p := buildFilter(aq.Tables, &f)
		frags = append(frags, frag)
		params = append(params, p...)
	}

	// 4. Raw SQL filters (pre-built fragments).
	for _, rf := range aq.RawFilters {
		frags = append(frags, rf.SQL)
		params = append(params, rf.Params...)
	}

	return frags, params
}

// filterOps maps Datalog function names to SQL operators.
var filterOps = map[string]string{
	">":  ">",
	"<":  "<",
	">=": ">=",
	"<=": "<=",
	"=":  "=",
	"!=": "!=",
}

func buildFilter(tables []TableBinding, f *Filter) (string, []any) {
	op, isBuiltin := filterOps[f.Fn]

	var params []any
	refs := make([]string, len(f.Args))
	for i, arg := range f.Args {
		if arg.Variable != "" {
			col := arg.Column
			if col == "v" && arg.ValueType != 0 {
				if tc := TypedColumn(arg.ValueType); tc != "" {
					col = tc
				}
			}
			refs[i] = tables[arg.Table].Alias + "." + col
		} else {
			refs[i] = "?"
			params = append(params, arg.Constant)
		}
	}

	if isBuiltin {
		if len(refs) == 2 {
			return refs[0] + " " + op + " " + refs[1], params
		}
		return strings.Join(refs, " "+op+" "), params
	}

	// UDF call: fn(arg1, arg2, ...) — treated as boolean predicate
	// Quote function name to support hyphenated identifiers (Datalog convention).
	quotedFn := `"` + f.Fn + `"`
	return quotedFn + "(" + strings.Join(refs, ", ") + ")", params
}

// ---------------------------------------------------------------------------
// NOT EXISTS
// ---------------------------------------------------------------------------

func buildNotExists(outer *AlgebraicQuery, nc *AlgebraicNot) (string, []any, error) {
	inner := nc.Inner

	if len(inner.Tables) == 0 {
		return "", nil, fmt.Errorf("translate NOT: empty inner subquery")
	}

	var frags []string
	var params []any

	// Inner FROM tables.
	var innerTables []string
	for _, tb := range inner.Tables {
		innerTables = append(innerTables, tb.Table+" AS "+tb.Alias)
	}

	// Inner WHERE: table bindings.
	for _, tb := range inner.Tables {
		if tb.AttrID != 0 {
			frags = append(frags, tb.Alias+".a = ?")
			params = append(params, tb.AttrID)
		}
		if tb.EBound != nil {
			frags = append(frags, tb.Alias+".e = ?")
			params = append(params, tb.EBound)
		}
		if tb.VBound != nil {
			if tc := TypedColumn(tb.ValueType); tc != "" {
				frags = append(frags, tb.Alias+"."+tc+" = ?")
			} else {
				frags = append(frags, tb.Alias+".v = ?")
			}
			params = append(params, tb.VBound)
		}
		if tb.TxBound != nil {
			frags = append(frags, tb.Alias+".tx = ?")
			params = append(params, tb.TxBound)
		}
	}

	// Inner joins (within the subquery).
	for _, jc := range inner.Joins {
		// The algebrizer's buildSubqueryJoins produces JoinConditions where
		// LeftTable indexes into the *outer* query's Tables and RightTable
		// indexes into the *inner* query's Tables. Detect this by checking
		// whether LeftTable is out of range for the inner tables.
		if jc.LeftTable < len(inner.Tables) {
			left := inner.Tables[jc.LeftTable].Alias + "." + jc.LeftCol
			right := inner.Tables[jc.RightTable].Alias + "." + jc.RightCol
			frags = append(frags, left+" = "+right)
		} else {
			// Correlation join: left references outer table, right references inner.
			left := outer.Tables[jc.LeftTable].Alias + "." + jc.LeftCol
			right := inner.Tables[jc.RightTable].Alias + "." + jc.RightCol
			frags = append(frags, left+" = "+right)
		}
	}

	// Inner filters.
	for _, f := range inner.Filters {
		frag, p := buildFilter(inner.Tables, &f)
		frags = append(frags, frag)
		params = append(params, p...)
	}

	whereClause := ""
	if len(frags) > 0 {
		whereClause = " WHERE " + strings.Join(frags, " AND ")
	}

	sql := fmt.Sprintf("NOT EXISTS (SELECT 1 FROM %s%s)",
		strings.Join(innerTables, ", "),
		whereClause)

	return sql, params, nil
}

// ---------------------------------------------------------------------------
// OR via UNION ALL
// ---------------------------------------------------------------------------

func translateWithOr(aq *AlgebraicQuery) (*SQLQuery, error) {
	// Each OR clause's branches are translated independently and combined
	// with UNION ALL. For multiple OR clauses this nests, but in practice
	// queries rarely have more than one.
	for _, oc := range aq.OrClauses {
		var unionParts []string
		var allParams []any

		for _, branch := range oc.Branches {
			sq, err := Translate(branch)
			if err != nil {
				return nil, fmt.Errorf("translate OR branch: %w", err)
			}
			unionParts = append(unionParts, sq.SQL)
			allParams = append(allParams, sq.Params...)
		}

		return &SQLQuery{
			SQL:    strings.Join(unionParts, "\nUNION ALL\n"),
			Params: allParams,
		}, nil
	}

	// Unreachable — we only enter this function when len(OrClauses) > 0.
	return translateCore(aq)
}

// ---------------------------------------------------------------------------
// GROUP BY
// ---------------------------------------------------------------------------

func buildGroupBy(aq *AlgebraicQuery) []string {
	var cols []string
	for _, fm := range aq.Find {
		if fm.Aggregate == "" {
			alias := aq.Tables[fm.Table].Alias
			cols = append(cols, alias+"."+fm.Column)
		}
	}
	return cols
}

// ---------------------------------------------------------------------------
// ORDER BY
// ---------------------------------------------------------------------------

func buildOrderBy(aq *AlgebraicQuery) string {
	var clauses []string
	for _, om := range aq.OrderBy {
		ref := aq.Tables[om.Table].Alias + "." + om.Column
		dir := "ASC"
		if om.Desc {
			dir = "DESC"
		}
		clauses = append(clauses, ref+" "+dir)
	}
	if len(clauses) == 0 {
		return ""
	}
	return "ORDER BY " + strings.Join(clauses, ", ")
}
