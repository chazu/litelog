package query

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Mock schema for algebrize tests
// ---------------------------------------------------------------------------

type mockSchema struct {
	attrs map[string]*AttrInfo
}

func (m *mockSchema) LookupAttribute(ident string) *AttrInfo {
	return m.attrs[ident]
}

func testSchema() *mockSchema {
	return &mockSchema{
		attrs: map[string]*AttrInfo{
			":person/name": {
				ID:          1000,
				Ident:       ":person/name",
				ValueType:   TypeString,
				Cardinality: 1,
				Indexed:     true,
				Unique:      true,
			},
			":person/age": {
				ID:          1001,
				Ident:       ":person/age",
				ValueType:   TypeInt,
				Cardinality: 1,
				Indexed:     false,
			},
			":parent": {
				ID:          1002,
				Ident:       ":parent",
				ValueType:   TypeRef,
				Cardinality: 2,
				Indexed:     false,
			},
		},
	}
}

// ===========================================================================
// ParseQuery tests
// ===========================================================================

func TestParseQuery_SimpleFindWhere(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :where [?e :person/name ?name]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// :find has 1 element
	if len(q.Find) != 1 {
		t.Fatalf("expected 1 find element, got %d", len(q.Find))
	}
	if q.Find[0].Variable != "?name" {
		t.Errorf("expected find variable ?name, got %q", q.Find[0].Variable)
	}
	if q.Find[0].Aggregate != "" {
		t.Errorf("expected no aggregate, got %q", q.Find[0].Aggregate)
	}

	// :where has 1 pattern
	if len(q.Where) != 1 {
		t.Fatalf("expected 1 where clause, got %d", len(q.Where))
	}
	p, ok := q.Where[0].(Pattern)
	if !ok {
		t.Fatalf("expected Pattern, got %T", q.Where[0])
	}

	// E is variable ?e
	if p.E.Variable != "?e" {
		t.Errorf("expected E variable ?e, got %q", p.E.Variable)
	}
	// A is constant :person/name
	if p.A.Constant != ":person/name" {
		t.Errorf("expected A constant :person/name, got %v", p.A.Constant)
	}
	// V is variable ?name
	if p.V.Variable != "?name" {
		t.Errorf("expected V variable ?name, got %q", p.V.Variable)
	}
}

func TestParseQuery_MultiPatternJoin(t *testing.T) {
	q, err := ParseQuery(`[:find ?name ?age :where [?e :person/name ?name] [?e :person/age ?age]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(q.Find) != 2 {
		t.Fatalf("expected 2 find elements, got %d", len(q.Find))
	}
	if q.Find[0].Variable != "?name" {
		t.Errorf("expected ?name, got %q", q.Find[0].Variable)
	}
	if q.Find[1].Variable != "?age" {
		t.Errorf("expected ?age, got %q", q.Find[1].Variable)
	}

	if len(q.Where) != 2 {
		t.Fatalf("expected 2 where clauses, got %d", len(q.Where))
	}

	// Both patterns should share ?e as entity variable
	p0 := q.Where[0].(Pattern)
	p1 := q.Where[1].(Pattern)
	if p0.E.Variable != "?e" || p1.E.Variable != "?e" {
		t.Errorf("expected both patterns to have ?e entity, got %q and %q", p0.E.Variable, p1.E.Variable)
	}
}

func TestParseQuery_WithInParams(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :in $ ?min-age :where [?e :person/name ?name]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(q.In) != 2 {
		t.Fatalf("expected 2 in specs, got %d", len(q.In))
	}
	// $ is a source
	if q.In[0].Variable != "$" || !q.In[0].Source {
		t.Errorf("expected $ source, got %+v", q.In[0])
	}
	// ?min-age is a parameter
	if q.In[1].Variable != "?min-age" || q.In[1].Source {
		t.Errorf("expected ?min-age param, got %+v", q.In[1])
	}
}

func TestParseQuery_WithOrderBy(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :where [?e :person/name ?name] :order-by [?name :asc]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(q.OrderBy) != 1 {
		t.Fatalf("expected 1 order-by clause, got %d", len(q.OrderBy))
	}
	if q.OrderBy[0].Variable != "?name" {
		t.Errorf("expected ?name, got %q", q.OrderBy[0].Variable)
	}
	if q.OrderBy[0].Desc {
		t.Error("expected ascending, got descending")
	}
}

func TestParseQuery_OrderByDesc(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :where [?e :person/name ?name] :order-by [?name :desc]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !q.OrderBy[0].Desc {
		t.Error("expected descending")
	}
}

func TestParseQuery_Aggregate(t *testing.T) {
	q, err := ParseQuery(`[:find (count ?e) :where [?e :person/name _]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(q.Find) != 1 {
		t.Fatalf("expected 1 find element, got %d", len(q.Find))
	}
	fe := q.Find[0]
	if fe.Aggregate != "count" {
		t.Errorf("expected aggregate count, got %q", fe.Aggregate)
	}
	if fe.AggArg != "?e" {
		t.Errorf("expected agg arg ?e, got %q", fe.AggArg)
	}
	if fe.Variable != "" {
		t.Errorf("expected no variable for aggregate, got %q", fe.Variable)
	}
}

func TestParseQuery_ExprClause(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :where [?e :person/name ?name] [?e :person/age ?age] [(> ?age 21)]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(q.Where) != 3 {
		t.Fatalf("expected 3 where clauses, got %d", len(q.Where))
	}

	ec, ok := q.Where[2].(ExprClause)
	if !ok {
		t.Fatalf("expected ExprClause, got %T", q.Where[2])
	}
	if ec.Fn != ">" {
		t.Errorf("expected fn >, got %q", ec.Fn)
	}
	if len(ec.Args) != 2 {
		t.Fatalf("expected 2 args, got %d", len(ec.Args))
	}
	if ec.Args[0].Variable != "?age" {
		t.Errorf("expected first arg ?age, got %q", ec.Args[0].Variable)
	}
	if ec.Args[1].Constant != int64(21) {
		t.Errorf("expected second arg 21, got %v (type %T)", ec.Args[1].Constant, ec.Args[1].Constant)
	}
}

func TestParseQuery_NotClause(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :where [?e :person/name ?name] (not [?e :person/age 30])]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(q.Where) != 2 {
		t.Fatalf("expected 2 where clauses, got %d", len(q.Where))
	}

	nc, ok := q.Where[1].(NotClause)
	if !ok {
		t.Fatalf("expected NotClause, got %T", q.Where[1])
	}
	if len(nc.Clauses) != 1 {
		t.Fatalf("expected 1 inner clause, got %d", len(nc.Clauses))
	}

	inner, ok := nc.Clauses[0].(Pattern)
	if !ok {
		t.Fatalf("expected inner Pattern, got %T", nc.Clauses[0])
	}
	if inner.E.Variable != "?e" {
		t.Errorf("expected inner E ?e, got %q", inner.E.Variable)
	}
	if inner.A.Constant != ":person/age" {
		t.Errorf("expected inner A :person/age, got %v", inner.A.Constant)
	}
	if inner.V.Constant != int64(30) {
		t.Errorf("expected inner V 30, got %v", inner.V.Constant)
	}
}

func TestParseQuery_OrClause(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :where [?e :person/name ?name] (or [?e :person/age 25] [?e :person/age 30])]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(q.Where) != 2 {
		t.Fatalf("expected 2 where clauses, got %d", len(q.Where))
	}

	oc, ok := q.Where[1].(OrClause)
	if !ok {
		t.Fatalf("expected OrClause, got %T", q.Where[1])
	}
	if len(oc.Branches) != 2 {
		t.Fatalf("expected 2 branches, got %d", len(oc.Branches))
	}

	// Each branch has 1 clause
	for i, branch := range oc.Branches {
		if len(branch) != 1 {
			t.Fatalf("branch %d: expected 1 clause, got %d", i, len(branch))
		}
		p, ok := branch[0].(Pattern)
		if !ok {
			t.Fatalf("branch %d: expected Pattern, got %T", i, branch[0])
		}
		if p.E.Variable != "?e" {
			t.Errorf("branch %d: expected E ?e, got %q", i, p.E.Variable)
		}
	}

	// Check specific constant values
	b0 := oc.Branches[0][0].(Pattern)
	b1 := oc.Branches[1][0].(Pattern)
	if b0.V.Constant != int64(25) {
		t.Errorf("branch 0: expected V 25, got %v", b0.V.Constant)
	}
	if b1.V.Constant != int64(30) {
		t.Errorf("branch 1: expected V 30, got %v", b1.V.Constant)
	}
}

func TestParseQuery_BlankElement(t *testing.T) {
	q, err := ParseQuery(`[:find ?e :where [?e :person/name _]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := q.Where[0].(Pattern)
	if !p.V.Blank {
		t.Error("expected V to be blank")
	}
	if p.V.Variable != "" {
		t.Errorf("expected no variable for blank, got %q", p.V.Variable)
	}
}

func TestParseQuery_MissingFind(t *testing.T) {
	_, err := ParseQuery(`[:where [?e :person/name ?name]]`)
	if err == nil {
		t.Fatal("expected error for missing :find")
	}
	if !strings.Contains(err.Error(), "missing :find") {
		t.Errorf("expected 'missing :find' in error, got %q", err.Error())
	}
}

func TestParseQuery_EmptyQuery(t *testing.T) {
	_, err := ParseQuery(`[]`)
	if err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestParseQuery_FourElementPatternWithTx(t *testing.T) {
	q, err := ParseQuery(`[:find ?v :where [?e :person/name ?v ?tx]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	p := q.Where[0].(Pattern)
	if p.Tx.Variable != "?tx" {
		t.Errorf("expected Tx variable ?tx, got %q", p.Tx.Variable)
	}
	if p.E.Variable != "?e" {
		t.Errorf("expected E variable ?e, got %q", p.E.Variable)
	}
}

// ===========================================================================
// ParseRule tests
// ===========================================================================

func TestParseRule_Simple(t *testing.T) {
	r, err := ParseRule(`[(ancestor ?x ?y) [?x :parent ?y]]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.Name != "ancestor" {
		t.Errorf("expected rule name ancestor, got %q", r.Name)
	}
	if len(r.Head) != 2 {
		t.Fatalf("expected 2 head args, got %d", len(r.Head))
	}
	if r.Head[0].Variable != "?x" {
		t.Errorf("expected head arg 0 ?x, got %q", r.Head[0].Variable)
	}
	if r.Head[1].Variable != "?y" {
		t.Errorf("expected head arg 1 ?y, got %q", r.Head[1].Variable)
	}

	if len(r.Body) != 1 {
		t.Fatalf("expected 1 body clause, got %d", len(r.Body))
	}
	p, ok := r.Body[0].(Pattern)
	if !ok {
		t.Fatalf("expected Pattern, got %T", r.Body[0])
	}
	if p.E.Variable != "?x" {
		t.Errorf("expected body E ?x, got %q", p.E.Variable)
	}
	if p.A.Constant != ":parent" {
		t.Errorf("expected body A :parent, got %v", p.A.Constant)
	}
	if p.V.Variable != "?y" {
		t.Errorf("expected body V ?y, got %q", p.V.Variable)
	}
}

func TestParseRule_Recursive(t *testing.T) {
	r, err := ParseRule(`[(ancestor ?x ?z) [?x :parent ?y] (ancestor ?y ?z)]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if r.Name != "ancestor" {
		t.Errorf("expected rule name ancestor, got %q", r.Name)
	}
	if len(r.Body) != 2 {
		t.Fatalf("expected 2 body clauses, got %d", len(r.Body))
	}

	// First body clause is a Pattern
	_, ok := r.Body[0].(Pattern)
	if !ok {
		t.Fatalf("expected first body clause to be Pattern, got %T", r.Body[0])
	}

	// Second body clause is a RuleApplication
	ra, ok := r.Body[1].(RuleApplication)
	if !ok {
		t.Fatalf("expected second body clause to be RuleApplication, got %T", r.Body[1])
	}
	if ra.Name != "ancestor" {
		t.Errorf("expected rule application name ancestor, got %q", ra.Name)
	}
	if len(ra.Args) != 2 {
		t.Fatalf("expected 2 rule args, got %d", len(ra.Args))
	}
	if ra.Args[0].Variable != "?y" {
		t.Errorf("expected rule arg 0 ?y, got %q", ra.Args[0].Variable)
	}
	if ra.Args[1].Variable != "?z" {
		t.Errorf("expected rule arg 1 ?z, got %q", ra.Args[1].Variable)
	}
}

func TestParseRules_Multiple(t *testing.T) {
	rules, err := ParseRules(`[
		[(ancestor ?x ?y) [?x :parent ?y]]
		[(ancestor ?x ?z) [?x :parent ?y] (ancestor ?y ?z)]
	]`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}
	if rules[0].Name != "ancestor" || rules[1].Name != "ancestor" {
		t.Errorf("expected both rules named ancestor, got %q and %q", rules[0].Name, rules[1].Name)
	}

	// First rule is base case (1 body clause)
	if len(rules[0].Body) != 1 {
		t.Errorf("expected base rule to have 1 body clause, got %d", len(rules[0].Body))
	}
	// Second rule is recursive (2 body clauses)
	if len(rules[1].Body) != 2 {
		t.Errorf("expected recursive rule to have 2 body clauses, got %d", len(rules[1].Body))
	}
}

// ===========================================================================
// Algebrize tests
// ===========================================================================

func TestAlgebrize_SimpleQuery(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :where [?e :person/name ?name]]`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize error: %v", err)
	}

	// Should have 1 table binding
	if len(aq.Tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(aq.Tables))
	}
	tb := aq.Tables[0]
	if tb.AttrID != 1000 {
		t.Errorf("expected AttrID 1000, got %d", tb.AttrID)
	}
	if tb.ValueType != TypeString {
		t.Errorf("expected ValueType TypeString (%d), got %d", TypeString, tb.ValueType)
	}
	if tb.Alias != "d0" {
		t.Errorf("expected alias d0, got %q", tb.Alias)
	}

	// No joins (single pattern)
	if len(aq.Joins) != 0 {
		t.Errorf("expected 0 joins, got %d", len(aq.Joins))
	}

	// Find mapping
	if len(aq.Find) != 1 {
		t.Fatalf("expected 1 find mapping, got %d", len(aq.Find))
	}
	fm := aq.Find[0]
	if fm.Variable != "?name" {
		t.Errorf("expected find variable ?name, got %q", fm.Variable)
	}
	if fm.Table != 0 {
		t.Errorf("expected find table 0, got %d", fm.Table)
	}
	if fm.Column != "v" {
		t.Errorf("expected find column v, got %q", fm.Column)
	}
	if fm.ValueType != TypeString {
		t.Errorf("expected find ValueType TypeString (%d), got %d", TypeString, fm.ValueType)
	}
}

func TestAlgebrize_JoinConditions(t *testing.T) {
	q, err := ParseQuery(`[:find ?name ?age :where [?e :person/name ?name] [?e :person/age ?age]]`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize error: %v", err)
	}

	if len(aq.Tables) != 2 {
		t.Fatalf("expected 2 tables, got %d", len(aq.Tables))
	}

	// Should have 1 join condition on ?e
	if len(aq.Joins) != 1 {
		t.Fatalf("expected 1 join, got %d", len(aq.Joins))
	}
	jc := aq.Joins[0]
	if jc.Variable != "?e" {
		t.Errorf("expected join on ?e, got %q", jc.Variable)
	}
	if jc.LeftCol != "e" || jc.RightCol != "e" {
		t.Errorf("expected join on e columns, got %q and %q", jc.LeftCol, jc.RightCol)
	}
	if jc.LeftTable != 0 || jc.RightTable != 1 {
		t.Errorf("expected join tables 0,1 got %d,%d", jc.LeftTable, jc.RightTable)
	}
}

func TestAlgebrize_FindMapping(t *testing.T) {
	q, err := ParseQuery(`[:find ?name ?age :where [?e :person/name ?name] [?e :person/age ?age]]`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize error: %v", err)
	}

	if len(aq.Find) != 2 {
		t.Fatalf("expected 2 find mappings, got %d", len(aq.Find))
	}

	// ?name -> table 0, column v, TypeString
	if aq.Find[0].Table != 0 || aq.Find[0].Column != "v" || aq.Find[0].ValueType != TypeString {
		t.Errorf("?name mapping wrong: table=%d col=%q type=%d", aq.Find[0].Table, aq.Find[0].Column, aq.Find[0].ValueType)
	}

	// ?age -> table 1, column v, TypeInt
	if aq.Find[1].Table != 1 || aq.Find[1].Column != "v" || aq.Find[1].ValueType != TypeInt {
		t.Errorf("?age mapping wrong: table=%d col=%q type=%d", aq.Find[1].Table, aq.Find[1].Column, aq.Find[1].ValueType)
	}
}

func TestAlgebrize_IndexHints(t *testing.T) {
	// Entity-bound pattern => eavt
	q1, _ := ParseQuery(`[:find ?v :where [42 :person/name ?v]]`)
	aq1, err := Algebrize(q1, testSchema())
	if err != nil {
		t.Fatalf("algebrize error: %v", err)
	}
	if aq1.Tables[0].IndexHint != "eavt" {
		t.Errorf("entity-bound: expected eavt, got %q", aq1.Tables[0].IndexHint)
	}

	// Attr-bound only => aevt
	q2, _ := ParseQuery(`[:find ?v :where [?e :person/age ?v]]`)
	aq2, err := Algebrize(q2, testSchema())
	if err != nil {
		t.Fatalf("algebrize error: %v", err)
	}
	if aq2.Tables[0].IndexHint != "aevt" {
		t.Errorf("attr-bound (not indexed): expected aevt, got %q", aq2.Tables[0].IndexHint)
	}
}

func TestAlgebrize_UnknownAttribute(t *testing.T) {
	q, _ := ParseQuery(`[:find ?v :where [?e :unknown/attr ?v]]`)
	_, err := Algebrize(q, testSchema())
	if err == nil {
		t.Fatal("expected error for unknown attribute")
	}
	if !strings.Contains(err.Error(), "unknown attribute") {
		t.Errorf("expected 'unknown attribute' in error, got %q", err.Error())
	}
}

func TestAlgebrize_VariableTypeInference(t *testing.T) {
	q, _ := ParseQuery(`[:find ?name :where [?e :person/name ?name]]`)
	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize error: %v", err)
	}

	// ?name should be inferred as TypeString from :person/name
	if aq.Find[0].ValueType != TypeString {
		t.Errorf("expected ?name type TypeString (%d), got %d", TypeString, aq.Find[0].ValueType)
	}
}

func TestAlgebrize_AggregateQuery(t *testing.T) {
	q, _ := ParseQuery(`[:find (count ?e) :where [?e :person/name _]]`)
	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize error: %v", err)
	}

	if !aq.HasAggregates {
		t.Error("expected HasAggregates to be true")
	}
	if aq.Find[0].Aggregate != "count" {
		t.Errorf("expected aggregate count, got %q", aq.Find[0].Aggregate)
	}
}

func TestAlgebrize_NotClause(t *testing.T) {
	q, _ := ParseQuery(`[:find ?name :where [?e :person/name ?name] (not [?e :person/age 30])]`)
	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize error: %v", err)
	}

	if len(aq.NotClauses) != 1 {
		t.Fatalf("expected 1 not clause, got %d", len(aq.NotClauses))
	}

	inner := aq.NotClauses[0].Inner
	if len(inner.Tables) != 1 {
		t.Fatalf("expected 1 inner table, got %d", len(inner.Tables))
	}
	if inner.Tables[0].AttrID != 1001 {
		t.Errorf("expected inner AttrID 1001, got %d", inner.Tables[0].AttrID)
	}
}

func TestAlgebrize_Filter(t *testing.T) {
	q, _ := ParseQuery(`[:find ?name :where [?e :person/name ?name] [?e :person/age ?age] [(> ?age 21)]]`)
	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize error: %v", err)
	}

	if len(aq.Filters) != 1 {
		t.Fatalf("expected 1 filter, got %d", len(aq.Filters))
	}
	f := aq.Filters[0]
	if f.Fn != ">" {
		t.Errorf("expected filter fn >, got %q", f.Fn)
	}
	if len(f.Args) != 2 {
		t.Fatalf("expected 2 filter args, got %d", len(f.Args))
	}
	if f.Args[0].Variable != "?age" {
		t.Errorf("expected first arg variable ?age, got %q", f.Args[0].Variable)
	}
	if f.Args[1].Constant != int64(21) {
		t.Errorf("expected second arg constant 21, got %v", f.Args[1].Constant)
	}
}

// ===========================================================================
// Translate tests
// ===========================================================================

func TestTranslate_SimpleQuery(t *testing.T) {
	aq := &AlgebraicQuery{
		Find: []FindMapping{
			{Variable: "?name", Table: 0, Column: "v", ValueType: TypeString},
		},
		Tables: []TableBinding{
			{Alias: "d0", Table: "current_datoms", AttrID: 1000, IndexHint: "aevt"},
		},
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate error: %v", err)
	}

	// Should contain SELECT DISTINCT
	if !strings.Contains(sq.SQL, "SELECT DISTINCT") {
		t.Errorf("expected DISTINCT for non-aggregate, got: %s", sq.SQL)
	}
	// Should contain FROM clause
	if !strings.Contains(sq.SQL, "FROM current_datoms AS d0") {
		t.Errorf("expected FROM clause, got: %s", sq.SQL)
	}
	// Should contain WHERE with attr binding
	if !strings.Contains(sq.SQL, "d0.a = ?") {
		t.Errorf("expected attr binding in WHERE, got: %s", sq.SQL)
	}
	// Params should have the attr ID
	if len(sq.Params) != 1 || sq.Params[0] != int64(1000) {
		t.Errorf("expected params [1000], got %v", sq.Params)
	}
}

func TestTranslate_TwoTableJoin(t *testing.T) {
	aq := &AlgebraicQuery{
		Find: []FindMapping{
			{Variable: "?name", Table: 0, Column: "v", ValueType: TypeString},
			{Variable: "?age", Table: 1, Column: "v", ValueType: TypeInt},
		},
		Tables: []TableBinding{
			{Alias: "d0", Table: "current_datoms", AttrID: 1000, IndexHint: "aevt"},
			{Alias: "d1", Table: "current_datoms", AttrID: 1001, IndexHint: "aevt"},
		},
		Joins: []JoinCondition{
			{Variable: "?e", LeftTable: 0, LeftCol: "e", RightTable: 1, RightCol: "e"},
		},
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate error: %v", err)
	}

	// FROM should have both tables
	if !strings.Contains(sq.SQL, "d0") || !strings.Contains(sq.SQL, "d1") {
		t.Errorf("expected both table aliases in SQL, got: %s", sq.SQL)
	}
	// WHERE should have join condition
	if !strings.Contains(sq.SQL, "d0.e = d1.e") {
		t.Errorf("expected join condition d0.e = d1.e, got: %s", sq.SQL)
	}
	// SELECT should include both columns
	if !strings.Contains(sq.SQL, "d0.v") || !strings.Contains(sq.SQL, "d1.v") {
		t.Errorf("expected both value columns in SELECT, got: %s", sq.SQL)
	}
}

func TestTranslate_WithFilter(t *testing.T) {
	aq := &AlgebraicQuery{
		Find: []FindMapping{
			{Variable: "?name", Table: 0, Column: "v", ValueType: TypeString},
		},
		Tables: []TableBinding{
			{Alias: "d0", Table: "current_datoms", AttrID: 1000},
			{Alias: "d1", Table: "current_datoms", AttrID: 1001},
		},
		Joins: []JoinCondition{
			{Variable: "?e", LeftTable: 0, LeftCol: "e", RightTable: 1, RightCol: "e"},
		},
		Filters: []Filter{
			{
				Fn: ">",
				Args: []FilterArg{
					{Variable: "?age", Table: 1, Column: "v"},
					{Constant: int64(21)},
				},
			},
		},
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate error: %v", err)
	}

	if !strings.Contains(sq.SQL, "d1.v > ?") {
		t.Errorf("expected filter d1.v > ?, got: %s", sq.SQL)
	}

	// Params: attr0, attr1, filter constant
	// attr bindings come first, then filter
	if len(sq.Params) != 3 {
		t.Fatalf("expected 3 params, got %d: %v", len(sq.Params), sq.Params)
	}
	// Last param should be the filter constant
	if sq.Params[2] != int64(21) {
		t.Errorf("expected last param 21, got %v", sq.Params[2])
	}
}

func TestTranslate_NotExists(t *testing.T) {
	inner := &AlgebraicQuery{
		Tables: []TableBinding{
			{Alias: "d0", Table: "current_datoms", AttrID: 1001, VBound: int64(30)},
		},
		Joins: []JoinCondition{
			// Correlation join: outer table 0, inner table 0
			{Variable: "?e", LeftTable: 0, LeftCol: "e", RightTable: 0, RightCol: "e"},
		},
	}

	aq := &AlgebraicQuery{
		Find: []FindMapping{
			{Variable: "?name", Table: 0, Column: "v", ValueType: TypeString},
		},
		Tables: []TableBinding{
			{Alias: "d0", Table: "current_datoms", AttrID: 1000},
		},
		NotClauses: []AlgebraicNot{
			{Inner: inner},
		},
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate error: %v", err)
	}

	if !strings.Contains(sq.SQL, "NOT EXISTS") {
		t.Errorf("expected NOT EXISTS in SQL, got: %s", sq.SQL)
	}
	if !strings.Contains(sq.SQL, "SELECT 1") {
		t.Errorf("expected SELECT 1 in subquery, got: %s", sq.SQL)
	}
}

func TestTranslate_AggregateGroupBy(t *testing.T) {
	aq := &AlgebraicQuery{
		Find: []FindMapping{
			{Variable: "?age", Table: 0, Column: "v", ValueType: TypeInt},
			{Variable: "", Table: 0, Column: "e", Aggregate: "count", AggArg: "?e"},
		},
		Tables: []TableBinding{
			{Alias: "d0", Table: "current_datoms", AttrID: 1001},
		},
		HasAggregates: true,
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate error: %v", err)
	}

	// Should NOT have DISTINCT (aggregate query)
	if strings.Contains(sq.SQL, "DISTINCT") {
		t.Errorf("expected no DISTINCT for aggregate query, got: %s", sq.SQL)
	}
	// Should have GROUP BY
	if !strings.Contains(sq.SQL, "GROUP BY") {
		t.Errorf("expected GROUP BY, got: %s", sq.SQL)
	}
	// Should have COUNT
	if !strings.Contains(sq.SQL, "COUNT(") {
		t.Errorf("expected COUNT in SELECT, got: %s", sq.SQL)
	}
}

func TestTranslate_IndexHintInFrom(t *testing.T) {
	aq := &AlgebraicQuery{
		Find: []FindMapping{
			{Variable: "?v", Table: 0, Column: "v"},
		},
		Tables: []TableBinding{
			{Alias: "d0", Table: "current_datoms", AttrID: 1000, IndexHint: "aevt"},
		},
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate error: %v", err)
	}

	if !strings.Contains(sq.SQL, "INDEXED BY aevt") {
		t.Errorf("expected INDEXED BY aevt, got: %s", sq.SQL)
	}
}

func TestTranslate_NoTables(t *testing.T) {
	aq := &AlgebraicQuery{}
	_, err := Translate(aq)
	if err == nil {
		t.Fatal("expected error for empty tables")
	}
}

func TestTranslate_OrderBy(t *testing.T) {
	aq := &AlgebraicQuery{
		Find: []FindMapping{
			{Variable: "?name", Table: 0, Column: "v"},
		},
		Tables: []TableBinding{
			{Alias: "d0", Table: "current_datoms", AttrID: 1000},
		},
		OrderBy: []OrderMapping{
			{Variable: "?name", Table: 0, Column: "v", Desc: true},
		},
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate error: %v", err)
	}

	if !strings.Contains(sq.SQL, "ORDER BY d0.v DESC") {
		t.Errorf("expected ORDER BY d0.v DESC, got: %s", sq.SQL)
	}
}

// ===========================================================================
// PlanRecursive tests
// ===========================================================================

func TestPlanRecursive_TransitiveClosure(t *testing.T) {
	rules, err := ParseRules(`[
		[(ancestor ?x ?y) [?x :parent ?y]]
		[(ancestor ?x ?z) [?x :parent ?y] (ancestor ?y ?z)]
	]`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	plan, err := PlanRecursive(rules, testSchema())
	if err != nil {
		t.Fatalf("plan error: %v", err)
	}

	// Setup should create temp tables
	if len(plan.SetupSQL) == 0 {
		t.Fatal("expected SetupSQL to be non-empty")
	}
	hasRuleTable := false
	hasDeltaTable := false
	for _, sql := range plan.SetupSQL {
		if strings.Contains(sql, "_rule_ancestor") {
			hasRuleTable = true
		}
		if strings.Contains(sql, "_delta_ancestor") {
			hasDeltaTable = true
		}
	}
	if !hasRuleTable {
		t.Error("expected _rule_ancestor in SetupSQL")
	}
	if !hasDeltaTable {
		t.Error("expected _delta_ancestor in SetupSQL")
	}

	// BaseSQL should have inserts from non-recursive rule
	if len(plan.BaseSQL) == 0 {
		t.Fatal("expected BaseSQL to be non-empty")
	}
	hasBaseInsert := false
	for _, sq := range plan.BaseSQL {
		if strings.Contains(sq.SQL, "INSERT OR IGNORE INTO _rule_ancestor") &&
			strings.Contains(sq.SQL, "current_datoms") {
			hasBaseInsert = true
		}
	}
	if !hasBaseInsert {
		t.Error("expected base INSERT into _rule_ancestor from current_datoms")
	}

	// DeltaSQL should use delta tables
	if len(plan.DeltaSQL) == 0 {
		t.Fatal("expected DeltaSQL to be non-empty")
	}
	hasDeltaRef := false
	for _, sq := range plan.DeltaSQL {
		if strings.Contains(sq.SQL, "_delta_ancestor") {
			hasDeltaRef = true
		}
	}
	if !hasDeltaRef {
		t.Error("expected _delta_ancestor reference in DeltaSQL")
	}

	// CleanupSQL should drop temp tables
	if len(plan.CleanupSQL) == 0 {
		t.Fatal("expected CleanupSQL to be non-empty")
	}
	hasRuleDrop := false
	hasDeltaDrop := false
	for _, sql := range plan.CleanupSQL {
		if strings.Contains(sql, "DROP TABLE IF EXISTS _rule_ancestor") {
			hasRuleDrop = true
		}
		if strings.Contains(sql, "DROP TABLE IF EXISTS _delta_ancestor") {
			hasDeltaDrop = true
		}
	}
	if !hasRuleDrop {
		t.Error("expected DROP _rule_ancestor in CleanupSQL")
	}
	if !hasDeltaDrop {
		t.Error("expected DROP _delta_ancestor in CleanupSQL")
	}

	// Result table
	if plan.ResultTable != "_rule_ancestor" {
		t.Errorf("expected ResultTable _rule_ancestor, got %q", plan.ResultTable)
	}

	// Columns
	if len(plan.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(plan.Columns))
	}
	if plan.Columns[0] != "c0" || plan.Columns[1] != "c1" {
		t.Errorf("expected columns [c0, c1], got %v", plan.Columns)
	}
}

func TestPlanRecursive_NoRules(t *testing.T) {
	_, err := PlanRecursive(nil, testSchema())
	if err == nil {
		t.Fatal("expected error for no rules")
	}
}

func TestPlanRecursive_InconsistentArity(t *testing.T) {
	rules, err := ParseRules(`[
		[(foo ?x ?y) [?x :parent ?y]]
		[(foo ?x) [?x :parent _]]
	]`)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}

	_, err = PlanRecursive(rules, testSchema())
	if err == nil {
		t.Fatal("expected error for inconsistent arity")
	}
	if !strings.Contains(err.Error(), "inconsistent arity") {
		t.Errorf("expected 'inconsistent arity' in error, got %q", err.Error())
	}
}

// ===========================================================================
// End-to-end: parse → algebrize → translate
// ===========================================================================

func TestEndToEnd_SimpleQuery(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :where [?e :person/name ?name]]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize: %v", err)
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	if !strings.Contains(sq.SQL, "SELECT") {
		t.Errorf("expected SELECT in SQL, got: %s", sq.SQL)
	}
	if !strings.Contains(sq.SQL, "current_datoms") {
		t.Errorf("expected current_datoms in SQL, got: %s", sq.SQL)
	}
	if len(sq.Params) == 0 {
		t.Error("expected at least 1 param (attr ID)")
	}
}

func TestEndToEnd_JoinQuery(t *testing.T) {
	q, err := ParseQuery(`[:find ?name ?age :where [?e :person/name ?name] [?e :person/age ?age]]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize: %v", err)
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	// Should join on entity
	if !strings.Contains(sq.SQL, "d0.e = d1.e") {
		t.Errorf("expected entity join in SQL, got: %s", sq.SQL)
	}

	// Should have 2 attr params
	if len(sq.Params) < 2 {
		t.Errorf("expected at least 2 params, got %d", len(sq.Params))
	}
}

func TestEndToEnd_FilterQuery(t *testing.T) {
	q, err := ParseQuery(`[:find ?name :where [?e :person/name ?name] [?e :person/age ?age] [(> ?age 21)]]`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	aq, err := Algebrize(q, testSchema())
	if err != nil {
		t.Fatalf("algebrize: %v", err)
	}

	sq, err := Translate(aq)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}

	if !strings.Contains(sq.SQL, "> ?") {
		t.Errorf("expected filter comparison in SQL, got: %s", sq.SQL)
	}
}
