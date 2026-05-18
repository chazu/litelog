package edn

import "strings"

// NodeType identifies the kind of AST node.
type NodeType int

const (
	NodeNil     NodeType = iota
	NodeBool             // true, false
	NodeInt              // 42, -7
	NodeFloat            // 3.14
	NodeString           // "hello"
	NodeSymbol           // regular symbol: ?x, foo
	NodeKeyword          // :keyword, :namespace/name
	NodeList             // (...)
	NodeVector           // [...]
	NodeMap              // {...}
	NodeSet              // #{...}
)

// Node is a single element in the EDN abstract syntax tree.
type Node struct {
	Type     NodeType
	Value    string  // raw text for atoms
	IntVal   int64   // parsed int (when Type == NodeInt)
	FloatVal float64 // parsed float (when Type == NodeFloat)
	Children []Node  // elements for collections
	Line     int
	Col      int
}

// AsKeyword returns the keyword name without the leading colon.
// Returns empty string if the node is not a keyword.
func (n Node) AsKeyword() string {
	if n.Type != NodeKeyword {
		return ""
	}
	return strings.TrimPrefix(n.Value, ":")
}

// AsSymbol returns the symbol name.
// Returns empty string if the node is not a symbol.
func (n Node) AsSymbol() string {
	if n.Type != NodeSymbol {
		return ""
	}
	return n.Value
}

// AsInt returns the parsed integer value.
func (n Node) AsInt() int64 {
	return n.IntVal
}

// AsString returns the string value.
// Returns empty string if the node is not a string.
func (n Node) AsString() string {
	if n.Type != NodeString {
		return ""
	}
	return n.Value
}

// IsVariable returns true if this is a symbol starting with '?'.
func (n Node) IsVariable() bool {
	return n.Type == NodeSymbol && len(n.Value) > 0 && n.Value[0] == '?'
}
