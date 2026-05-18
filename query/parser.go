package query

import (
	"fmt"
	"strings"

	"github.com/chazu/litelog/edn"
)

// ParseQuery parses a Datomic-style Datalog query from an EDN string.
// The input should be a vector: [:find ... :where ... ]
func ParseQuery(input string) (*Query, error) {
	node, err := edn.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("parse edn: %w", err)
	}
	if node.Type != edn.NodeVector {
		return nil, fmt.Errorf("query must be a vector [...], got %v", node.Type)
	}

	q := &Query{}
	children := node.Children

	// Split children into sections by keyword markers.
	sections := make(map[string][]edn.Node)
	var currentKey string
	for _, child := range children {
		if child.Type == edn.NodeKeyword {
			currentKey = child.Value
			sections[currentKey] = nil
			continue
		}
		if currentKey == "" {
			return nil, fmt.Errorf("line %d col %d: unexpected element before first keyword", child.Line, child.Col)
		}
		sections[currentKey] = append(sections[currentKey], child)
	}

	// Parse :find
	if findNodes, ok := sections[":find"]; ok {
		elems, err := parseFind(findNodes)
		if err != nil {
			return nil, fmt.Errorf(":find: %w", err)
		}
		q.Find = elems
	} else {
		return nil, fmt.Errorf("query missing :find clause")
	}

	// Parse :where
	if whereNodes, ok := sections[":where"]; ok {
		clauses, err := parseWhere(whereNodes)
		if err != nil {
			return nil, fmt.Errorf(":where: %w", err)
		}
		q.Where = clauses
	}

	// Parse :in
	if inNodes, ok := sections[":in"]; ok {
		specs, err := parseIn(inNodes)
		if err != nil {
			return nil, fmt.Errorf(":in: %w", err)
		}
		q.In = specs
	}

	// Parse :order-by
	if orderNodes, ok := sections[":order-by"]; ok {
		clauses, err := parseOrderBy(orderNodes)
		if err != nil {
			return nil, fmt.Errorf(":order-by: %w", err)
		}
		q.OrderBy = clauses
	}

	return q, nil
}

// ParseRule parses a single Datalog rule from EDN.
// Format: [(rule-name ?arg1 ?arg2) [?e :attr ?v] ...]
func ParseRule(input string) (*Rule, error) {
	node, err := edn.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("parse edn: %w", err)
	}
	return parseRuleNode(node)
}

// ParseRules parses multiple Datalog rules from EDN.
// Format: [[(rule1 ...) ...] [(rule2 ...) ...]]
func ParseRules(input string) ([]*Rule, error) {
	node, err := edn.Parse(input)
	if err != nil {
		return nil, fmt.Errorf("parse edn: %w", err)
	}
	if node.Type != edn.NodeVector {
		return nil, fmt.Errorf("rules must be a vector [...], got %v", node.Type)
	}

	var rules []*Rule
	for _, child := range node.Children {
		r, err := parseRuleNode(child)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

func parseRuleNode(node edn.Node) (*Rule, error) {
	if node.Type != edn.NodeVector {
		return nil, fmt.Errorf("line %d col %d: rule must be a vector", node.Line, node.Col)
	}
	if len(node.Children) < 1 {
		return nil, fmt.Errorf("line %d col %d: empty rule", node.Line, node.Col)
	}

	// First child should be a list: (rule-name ?arg1 ?arg2)
	head := node.Children[0]
	if head.Type != edn.NodeList {
		return nil, fmt.Errorf("line %d col %d: rule head must be a list (...)", head.Line, head.Col)
	}
	if len(head.Children) < 1 {
		return nil, fmt.Errorf("line %d col %d: empty rule head", head.Line, head.Col)
	}

	r := &Rule{}
	r.Name = head.Children[0].Value
	for _, arg := range head.Children[1:] {
		r.Head = append(r.Head, nodeToPatternElem(arg))
	}

	// Remaining children are body clauses
	for _, child := range node.Children[1:] {
		clause, err := parseClause(child)
		if err != nil {
			return nil, err
		}
		r.Body = append(r.Body, clause)
	}

	return r, nil
}

func parseFind(nodes []edn.Node) ([]FindElem, error) {
	var elems []FindElem
	for _, n := range nodes {
		switch n.Type {
		case edn.NodeSymbol:
			elems = append(elems, FindElem{Variable: n.Value})
		case edn.NodeList:
			// Aggregate: (count ?e), (sum ?amount)
			if len(n.Children) < 2 {
				return nil, fmt.Errorf("line %d col %d: aggregate must have function and argument", n.Line, n.Col)
			}
			fn := n.Children[0]
			arg := n.Children[1]
			elems = append(elems, FindElem{
				Aggregate: fn.Value,
				AggArg:    arg.Value,
			})
		default:
			return nil, fmt.Errorf("line %d col %d: unexpected element in :find", n.Line, n.Col)
		}
	}
	return elems, nil
}

func parseWhere(nodes []edn.Node) ([]Clause, error) {
	var clauses []Clause
	for _, n := range nodes {
		clause, err := parseClause(n)
		if err != nil {
			return nil, err
		}
		clauses = append(clauses, clause)
	}
	return clauses, nil
}

func parseClause(n edn.Node) (Clause, error) {
	switch n.Type {
	case edn.NodeVector:
		return parseVectorClause(n)
	case edn.NodeList:
		return parseListClause(n)
	default:
		return nil, fmt.Errorf("line %d col %d: where clause must be a vector or list", n.Line, n.Col)
	}
}

func parseVectorClause(n edn.Node) (Clause, error) {
	children := n.Children
	if len(children) == 0 {
		return nil, fmt.Errorf("line %d col %d: empty where clause", n.Line, n.Col)
	}

	// Check if first element is a list — expression clause: [(> ?age 21) ?result]
	if children[0].Type == edn.NodeList {
		return parseExpressionClause(n)
	}

	// Otherwise it's a data pattern: [?e :attr ?v] or [?e :attr ?v ?tx]
	if len(children) < 2 || len(children) > 4 {
		return nil, fmt.Errorf("line %d col %d: data pattern must have 2-4 elements, got %d", n.Line, n.Col, len(children))
	}

	p := Pattern{}
	p.E = nodeToPatternElem(children[0])
	p.A = nodeToPatternElem(children[1])
	if len(children) >= 3 {
		p.V = nodeToPatternElem(children[2])
	} else {
		p.V = PatternElem{Blank: true}
	}
	if len(children) >= 4 {
		p.Tx = nodeToPatternElem(children[3])
	}

	return p, nil
}

func parseExpressionClause(n edn.Node) (Clause, error) {
	children := n.Children
	fnList := children[0]
	if len(fnList.Children) < 1 {
		return nil, fmt.Errorf("line %d col %d: empty expression clause", fnList.Line, fnList.Col)
	}

	ec := ExprClause{
		Fn: fnList.Children[0].Value,
	}
	for _, arg := range fnList.Children[1:] {
		ec.Args = append(ec.Args, nodeToPatternElem(arg))
	}

	if len(children) >= 2 {
		ec.Binding = nodeToPatternElem(children[1])
	}

	return ec, nil
}

func parseListClause(n edn.Node) (Clause, error) {
	if len(n.Children) == 0 {
		return nil, fmt.Errorf("line %d col %d: empty list clause", n.Line, n.Col)
	}

	head := n.Children[0]
	switch head.Value {
	case "not":
		var clauses []Clause
		for _, child := range n.Children[1:] {
			c, err := parseClause(child)
			if err != nil {
				return nil, err
			}
			clauses = append(clauses, c)
		}
		return NotClause{Clauses: clauses}, nil

	case "or":
		var branches [][]Clause
		for _, child := range n.Children[1:] {
			c, err := parseClause(child)
			if err != nil {
				return nil, err
			}
			branches = append(branches, []Clause{c})
		}
		return OrClause{Branches: branches}, nil

	default:
		// Rule application: (rule-name ?arg1 ?arg2)
		ra := RuleApplication{Name: head.Value}
		for _, arg := range n.Children[1:] {
			ra.Args = append(ra.Args, nodeToPatternElem(arg))
		}
		return ra, nil
	}
}

func parseIn(nodes []edn.Node) ([]InputSpec, error) {
	var specs []InputSpec
	for _, n := range nodes {
		v := n.Value
		if v == "$" || strings.HasPrefix(v, "$") {
			specs = append(specs, InputSpec{Variable: v, Source: true})
		} else {
			specs = append(specs, InputSpec{Variable: v})
		}
	}
	return specs, nil
}

func parseOrderBy(nodes []edn.Node) ([]OrderClause, error) {
	var clauses []OrderClause
	for _, n := range nodes {
		if n.Type == edn.NodeVector && len(n.Children) == 2 {
			variable := n.Children[0].Value
			dir := n.Children[1].Value
			clauses = append(clauses, OrderClause{
				Variable: variable,
				Desc:     dir == ":desc",
			})
		} else if n.Type == edn.NodeSymbol {
			// bare variable means ascending
			clauses = append(clauses, OrderClause{Variable: n.Value})
		} else {
			return nil, fmt.Errorf("line %d col %d: invalid :order-by element", n.Line, n.Col)
		}
	}
	return clauses, nil
}

func nodeToPatternElem(n edn.Node) PatternElem {
	switch {
	case n.Value == "_":
		return PatternElem{Blank: true}
	case n.IsVariable():
		return PatternElem{Variable: n.Value}
	case n.Type == edn.NodeKeyword:
		return PatternElem{Constant: n.Value}
	case n.Type == edn.NodeInt:
		return PatternElem{Constant: n.IntVal}
	case n.Type == edn.NodeFloat:
		return PatternElem{Constant: n.FloatVal}
	case n.Type == edn.NodeString:
		return PatternElem{Constant: n.Value}
	case n.Type == edn.NodeBool:
		return PatternElem{Constant: n.Value == "true"}
	case n.Type == edn.NodeNil:
		return PatternElem{Constant: nil}
	default:
		// Symbols that aren't variables (e.g., function names, rule names)
		return PatternElem{Constant: n.Value}
	}
}
