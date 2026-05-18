package edn

import (
	"math"
	"testing"
)

// ---------------------------------------------------------------------------
// Lexer tests
// ---------------------------------------------------------------------------

func collectTokens(t *testing.T, input string) []Token {
	t.Helper()
	lex := NewLexer(input)
	var tokens []Token
	for {
		tok, err := lex.NextToken()
		if err != nil {
			t.Fatalf("unexpected lexer error on input %q: %v", input, err)
		}
		tokens = append(tokens, tok)
		if tok.Type == TokenEOF {
			break
		}
	}
	return tokens
}

func TestLexer_EmptyInput(t *testing.T) {
	tokens := collectTokens(t, "")
	if len(tokens) != 1 || tokens[0].Type != TokenEOF {
		t.Fatalf("expected single EOF token, got %v", tokens)
	}
}

func TestLexer_WhitespaceSkipping(t *testing.T) {
	inputs := []string{
		"   ",
		"\t\t",
		"\n\n",
		"  \t \n ",
		",,,",
		" , \t , \n ,",
	}
	for _, input := range inputs {
		tokens := collectTokens(t, input)
		if len(tokens) != 1 || tokens[0].Type != TokenEOF {
			t.Errorf("input %q: expected only EOF, got %v", input, tokens)
		}
	}
}

func TestLexer_LineComments(t *testing.T) {
	tokens := collectTokens(t, "; this is a comment\n42")
	// Expect Atom(42), EOF
	if len(tokens) != 2 {
		t.Fatalf("expected 2 tokens, got %d: %v", len(tokens), tokens)
	}
	if tokens[0].Type != TokenAtom || tokens[0].Value != "42" {
		t.Errorf("expected Atom(42), got %v", tokens[0])
	}
}

func TestLexer_CommentAtEOF(t *testing.T) {
	tokens := collectTokens(t, "; comment without newline")
	if len(tokens) != 1 || tokens[0].Type != TokenEOF {
		t.Errorf("expected only EOF after comment-at-EOF, got %v", tokens)
	}
}

func TestLexer_StringTokens(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`"hello"`, "hello"},
		{`"with spaces"`, "with spaces"},
		{`""`, ""},
		{`"line\nbreak"`, "line\nbreak"},
		{`"tab\there"`, "tab\there"},
		{`"back\\slash"`, "back\\slash"},
		{`"escaped\"quote"`, `escaped"quote`},
		{`"carriage\rreturn"`, "carriage\rreturn"},
	}
	for _, tt := range tests {
		tokens := collectTokens(t, tt.input)
		if len(tokens) < 1 || tokens[0].Type != TokenString {
			t.Errorf("input %s: expected string token, got %v", tt.input, tokens)
			continue
		}
		if tokens[0].Value != tt.want {
			t.Errorf("input %s: got value %q, want %q", tt.input, tokens[0].Value, tt.want)
		}
	}
}

func TestLexer_UnterminatedString(t *testing.T) {
	lex := NewLexer(`"no closing quote`)
	_, err := lex.NextToken()
	if err == nil {
		t.Fatal("expected error for unterminated string, got nil")
	}
}

func TestLexer_UnterminatedEscape(t *testing.T) {
	lex := NewLexer(`"trailing\`)
	_, err := lex.NextToken()
	if err == nil {
		t.Fatal("expected error for unterminated escape, got nil")
	}
}

func TestLexer_StructuralTokens(t *testing.T) {
	input := "()[]{}#"
	tokens := collectTokens(t, input)
	expected := []TokenType{
		TokenLParen, TokenRParen,
		TokenLBracket, TokenRBracket,
		TokenLBrace, TokenRBrace,
		TokenHash, TokenEOF,
	}
	if len(tokens) != len(expected) {
		t.Fatalf("expected %d tokens, got %d: %v", len(expected), len(tokens), tokens)
	}
	for i, want := range expected {
		if tokens[i].Type != want {
			t.Errorf("token %d: got type %d, want %d", i, tokens[i].Type, want)
		}
	}
}

func TestLexer_AtomTokens(t *testing.T) {
	tests := []struct {
		input string
		value string
	}{
		{"foo", "foo"},
		{":keyword", ":keyword"},
		{"42", "42"},
		{"-7", "-7"},
		{"3.14", "3.14"},
		{"nil", "nil"},
		{"true", "true"},
		{"false", "false"},
		{"?x", "?x"},
		{"+", "+"},
		{":namespace/name", ":namespace/name"},
	}
	for _, tt := range tests {
		tokens := collectTokens(t, tt.input)
		if tokens[0].Type != TokenAtom {
			t.Errorf("input %q: expected TokenAtom, got %d", tt.input, tokens[0].Type)
			continue
		}
		if tokens[0].Value != tt.value {
			t.Errorf("input %q: got %q, want %q", tt.input, tokens[0].Value, tt.value)
		}
	}
}

func TestLexer_LineAndColumn(t *testing.T) {
	// Verify position tracking across newlines
	tokens := collectTokens(t, "a\nb")
	if tokens[0].Line != 1 || tokens[0].Col != 1 {
		t.Errorf("first token: got line=%d col=%d, want 1,1", tokens[0].Line, tokens[0].Col)
	}
	if tokens[1].Line != 2 || tokens[1].Col != 1 {
		t.Errorf("second token: got line=%d col=%d, want 2,1", tokens[1].Line, tokens[1].Col)
	}
}

// ---------------------------------------------------------------------------
// Parser tests — Parse()
// ---------------------------------------------------------------------------

func TestParse_Nil(t *testing.T) {
	n, err := Parse("nil")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeNil {
		t.Errorf("expected NodeNil, got %d", n.Type)
	}
	if n.Value != "nil" {
		t.Errorf("expected value \"nil\", got %q", n.Value)
	}
}

func TestParse_Booleans(t *testing.T) {
	for _, input := range []string{"true", "false"} {
		n, err := Parse(input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", input, err)
		}
		if n.Type != NodeBool {
			t.Errorf("Parse(%q): expected NodeBool, got %d", input, n.Type)
		}
		if n.Value != input {
			t.Errorf("Parse(%q): expected value %q, got %q", input, input, n.Value)
		}
	}
}

func TestParse_Integers(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"42", 42},
		{"-7", -7},
		{"0", 0},
		{"999999", 999999},
	}
	for _, tt := range tests {
		n, err := Parse(tt.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.input, err)
		}
		if n.Type != NodeInt {
			t.Errorf("Parse(%q): expected NodeInt, got %d", tt.input, n.Type)
			continue
		}
		if n.IntVal != tt.want {
			t.Errorf("Parse(%q): IntVal = %d, want %d", tt.input, n.IntVal, tt.want)
		}
	}
}

func TestParse_Floats(t *testing.T) {
	tests := []struct {
		input string
		want  float64
	}{
		{"3.14", 3.14},
		{"-0.5", -0.5},
		{"0.0", 0.0},
		{"1e10", 1e10},
	}
	for _, tt := range tests {
		n, err := Parse(tt.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.input, err)
		}
		if n.Type != NodeFloat {
			t.Errorf("Parse(%q): expected NodeFloat, got %d", tt.input, n.Type)
			continue
		}
		if math.Abs(n.FloatVal-tt.want) > 1e-9 {
			t.Errorf("Parse(%q): FloatVal = %g, want %g", tt.input, n.FloatVal, tt.want)
		}
	}
}

func TestParse_Strings(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{`"hello"`, "hello"},
		{`"with \"escape\""`, `with "escape"`},
		{`""`, ""},
		{`"newline\n"`, "newline\n"},
	}
	for _, tt := range tests {
		n, err := Parse(tt.input)
		if err != nil {
			t.Fatalf("Parse(%s): %v", tt.input, err)
		}
		if n.Type != NodeString {
			t.Errorf("Parse(%s): expected NodeString, got %d", tt.input, n.Type)
			continue
		}
		if n.Value != tt.want {
			t.Errorf("Parse(%s): Value = %q, want %q", tt.input, n.Value, tt.want)
		}
	}
}

func TestParse_Keywords(t *testing.T) {
	tests := []struct {
		input string
		value string
	}{
		{":foo", ":foo"},
		{":namespace/name", ":namespace/name"},
		{":a", ":a"},
	}
	for _, tt := range tests {
		n, err := Parse(tt.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.input, err)
		}
		if n.Type != NodeKeyword {
			t.Errorf("Parse(%q): expected NodeKeyword, got %d", tt.input, n.Type)
		}
		if n.Value != tt.value {
			t.Errorf("Parse(%q): Value = %q, want %q", tt.input, n.Value, tt.value)
		}
	}
}

func TestParse_Symbols(t *testing.T) {
	tests := []struct {
		input string
		value string
	}{
		{"foo", "foo"},
		{"?x", "?x"},
		{"+", "+"},
		{"my-sym", "my-sym"},
	}
	for _, tt := range tests {
		n, err := Parse(tt.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.input, err)
		}
		if n.Type != NodeSymbol {
			t.Errorf("Parse(%q): expected NodeSymbol, got %d", tt.input, n.Type)
		}
		if n.Value != tt.value {
			t.Errorf("Parse(%q): Value = %q, want %q", tt.input, n.Value, tt.value)
		}
	}
}

func TestParse_Vectors(t *testing.T) {
	n, err := Parse("[1 2 3]")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeVector {
		t.Fatalf("expected NodeVector, got %d", n.Type)
	}
	if len(n.Children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(n.Children))
	}
	for i, want := range []int64{1, 2, 3} {
		if n.Children[i].Type != NodeInt || n.Children[i].IntVal != want {
			t.Errorf("child %d: got %+v, want int %d", i, n.Children[i], want)
		}
	}
}

func TestParse_Lists(t *testing.T) {
	n, err := Parse("(+ 1 2)")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeList {
		t.Fatalf("expected NodeList, got %d", n.Type)
	}
	if len(n.Children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(n.Children))
	}
	if n.Children[0].Type != NodeSymbol || n.Children[0].Value != "+" {
		t.Errorf("first child: expected Symbol(+), got %+v", n.Children[0])
	}
	if n.Children[1].Type != NodeInt || n.Children[1].IntVal != 1 {
		t.Errorf("second child: expected Int(1), got %+v", n.Children[1])
	}
}

func TestParse_Maps(t *testing.T) {
	n, err := Parse("{:a 1 :b 2}")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeMap {
		t.Fatalf("expected NodeMap, got %d", n.Type)
	}
	if len(n.Children) != 4 {
		t.Fatalf("expected 4 children (2 key-value pairs), got %d", len(n.Children))
	}
	if n.Children[0].Type != NodeKeyword || n.Children[0].Value != ":a" {
		t.Errorf("child 0: expected Keyword(:a), got %+v", n.Children[0])
	}
	if n.Children[1].Type != NodeInt || n.Children[1].IntVal != 1 {
		t.Errorf("child 1: expected Int(1), got %+v", n.Children[1])
	}
}

func TestParse_Sets(t *testing.T) {
	n, err := Parse("#{1 2 3}")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeSet {
		t.Fatalf("expected NodeSet, got %d", n.Type)
	}
	if len(n.Children) != 3 {
		t.Fatalf("expected 3 children, got %d", len(n.Children))
	}
}

func TestParse_NestedStructure(t *testing.T) {
	n, err := Parse("[:find ?x :where [?e :name ?x]]")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeVector {
		t.Fatalf("expected NodeVector, got %d", n.Type)
	}
	// [:find ?x :where [?e :name ?x]]
	// Children: :find, ?x, :where, [?e :name ?x]
	if len(n.Children) != 4 {
		t.Fatalf("expected 4 children, got %d", len(n.Children))
	}
	if n.Children[0].Type != NodeKeyword || n.Children[0].Value != ":find" {
		t.Errorf("child 0: expected Keyword(:find), got %+v", n.Children[0])
	}
	if n.Children[1].Type != NodeSymbol || n.Children[1].Value != "?x" {
		t.Errorf("child 1: expected Symbol(?x), got %+v", n.Children[1])
	}
	if n.Children[2].Type != NodeKeyword || n.Children[2].Value != ":where" {
		t.Errorf("child 2: expected Keyword(:where), got %+v", n.Children[2])
	}
	inner := n.Children[3]
	if inner.Type != NodeVector {
		t.Fatalf("child 3: expected NodeVector, got %d", inner.Type)
	}
	if len(inner.Children) != 3 {
		t.Fatalf("inner vector: expected 3 children, got %d", len(inner.Children))
	}
	if inner.Children[0].Value != "?e" {
		t.Errorf("inner[0]: expected ?e, got %q", inner.Children[0].Value)
	}
	if inner.Children[1].Value != ":name" {
		t.Errorf("inner[1]: expected :name, got %q", inner.Children[1].Value)
	}
	if inner.Children[2].Value != "?x" {
		t.Errorf("inner[2]: expected ?x, got %q", inner.Children[2].Value)
	}
}

func TestParse_EmptyCollections(t *testing.T) {
	tests := []struct {
		input    string
		nodeType NodeType
	}{
		{"[]", NodeVector},
		{"()", NodeList},
		{"{}", NodeMap},
		{"#{}", NodeSet},
	}
	for _, tt := range tests {
		n, err := Parse(tt.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.input, err)
		}
		if n.Type != tt.nodeType {
			t.Errorf("Parse(%q): expected type %d, got %d", tt.input, tt.nodeType, n.Type)
		}
		if len(n.Children) != 0 {
			t.Errorf("Parse(%q): expected 0 children, got %d", tt.input, len(n.Children))
		}
	}
}

func TestParse_MapOddElements(t *testing.T) {
	_, err := Parse("{:a 1 :b}")
	if err == nil {
		t.Fatal("expected error for map with odd number of elements, got nil")
	}
}

func TestParse_UnexpectedClosingDelimiter(t *testing.T) {
	for _, input := range []string{")", "]", "}"} {
		_, err := Parse(input)
		if err == nil {
			t.Errorf("Parse(%q): expected error for unexpected closing delimiter", input)
		}
	}
}

func TestParse_UnterminatedCollection(t *testing.T) {
	for _, input := range []string{"[1 2", "(1 2", "{:a 1"} {
		_, err := Parse(input)
		if err == nil {
			t.Errorf("Parse(%q): expected error for unterminated collection", input)
		}
	}
}

func TestParse_EmptyInput(t *testing.T) {
	_, err := Parse("")
	if err == nil {
		t.Fatal("expected error for empty input (unexpected end of input)")
	}
}

func TestParse_HashWithoutBrace(t *testing.T) {
	_, err := Parse("#foo")
	if err == nil {
		t.Fatal("expected error for # not followed by {")
	}
}

// ---------------------------------------------------------------------------
// ParseAll tests
// ---------------------------------------------------------------------------

func TestParseAll_MultipleValues(t *testing.T) {
	nodes, err := ParseAll("1 2 3")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}
	for i, want := range []int64{1, 2, 3} {
		if nodes[i].Type != NodeInt || nodes[i].IntVal != want {
			t.Errorf("node %d: expected Int(%d), got %+v", i, want, nodes[i])
		}
	}
}

func TestParseAll_MixedTypes(t *testing.T) {
	nodes, err := ParseAll(`:key [1 2] "str"`)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(nodes))
	}
	if nodes[0].Type != NodeKeyword {
		t.Errorf("node 0: expected NodeKeyword, got %d", nodes[0].Type)
	}
	if nodes[1].Type != NodeVector {
		t.Errorf("node 1: expected NodeVector, got %d", nodes[1].Type)
	}
	if nodes[2].Type != NodeString {
		t.Errorf("node 2: expected NodeString, got %d", nodes[2].Type)
	}
}

func TestParseAll_Empty(t *testing.T) {
	nodes, err := ParseAll("")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(nodes))
	}
}

func TestParseAll_OnlyWhitespace(t *testing.T) {
	nodes, err := ParseAll("   \n\t  ")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Errorf("expected 0 nodes, got %d", len(nodes))
	}
}

// ---------------------------------------------------------------------------
// Node accessor tests
// ---------------------------------------------------------------------------

func TestNode_IsVariable(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"?x", true},
		{"?foo-bar", true},
		{"x", false},
		{":key", false},
		{"42", false},
	}
	for _, tt := range tests {
		n, err := Parse(tt.input)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.input, err)
		}
		if got := n.IsVariable(); got != tt.want {
			t.Errorf("Parse(%q).IsVariable() = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestNode_AsKeyword(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{":foo", "foo"},
		{":ns/name", "ns/name"},
	}
	for _, tt := range tests {
		n, err := Parse(tt.input)
		if err != nil {
			t.Fatal(err)
		}
		if got := n.AsKeyword(); got != tt.want {
			t.Errorf("Parse(%q).AsKeyword() = %q, want %q", tt.input, got, tt.want)
		}
	}

	// Non-keyword returns empty string
	n, _ := Parse("foo")
	if got := n.AsKeyword(); got != "" {
		t.Errorf("symbol.AsKeyword() = %q, want empty", got)
	}
}

func TestNode_AsSymbol(t *testing.T) {
	n, _ := Parse("foo")
	if got := n.AsSymbol(); got != "foo" {
		t.Errorf("AsSymbol() = %q, want %q", got, "foo")
	}

	// Non-symbol returns empty string
	n, _ = Parse(":key")
	if got := n.AsSymbol(); got != "" {
		t.Errorf("keyword.AsSymbol() = %q, want empty", got)
	}
}

func TestNode_AsInt(t *testing.T) {
	n, _ := Parse("42")
	if got := n.AsInt(); got != 42 {
		t.Errorf("AsInt() = %d, want 42", got)
	}

	n, _ = Parse("-7")
	if got := n.AsInt(); got != -7 {
		t.Errorf("AsInt() = %d, want -7", got)
	}
}

func TestNode_AsString(t *testing.T) {
	n, _ := Parse(`"hello"`)
	if got := n.AsString(); got != "hello" {
		t.Errorf("AsString() = %q, want %q", got, "hello")
	}

	// Non-string returns empty
	n, _ = Parse("42")
	if got := n.AsString(); got != "" {
		t.Errorf("int.AsString() = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Edge cases
// ---------------------------------------------------------------------------

func TestEdge_UnicodeInStrings(t *testing.T) {
	n, err := Parse(`"héllo wörld 日本語"`)
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeString {
		t.Fatalf("expected NodeString, got %d", n.Type)
	}
	if n.Value != "héllo wörld 日本語" {
		t.Errorf("got %q", n.Value)
	}
}

func TestEdge_UnicodeInSymbols(t *testing.T) {
	n, err := Parse("αβγ")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeSymbol || n.Value != "αβγ" {
		t.Errorf("expected Symbol(αβγ), got %+v", n)
	}
}

func TestEdge_NegativeNumbers(t *testing.T) {
	n, err := Parse("-42")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeInt || n.IntVal != -42 {
		t.Errorf("expected Int(-42), got %+v", n)
	}

	n, err = Parse("-3.14")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeFloat || math.Abs(n.FloatVal-(-3.14)) > 1e-9 {
		t.Errorf("expected Float(-3.14), got %+v", n)
	}
}

func TestEdge_CommasAsWhitespace(t *testing.T) {
	n, err := Parse("[1, 2, 3]")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeVector || len(n.Children) != 3 {
		t.Fatalf("expected Vector with 3 children, got %+v", n)
	}
	for i, want := range []int64{1, 2, 3} {
		if n.Children[i].IntVal != want {
			t.Errorf("child %d: got %d, want %d", i, n.Children[i].IntVal, want)
		}
	}
}

func TestEdge_CommasEquivalent(t *testing.T) {
	// Verify [1, 2, 3] and [1 2 3] produce identical results
	n1, _ := Parse("[1, 2, 3]")
	n2, _ := Parse("[1 2 3]")
	if len(n1.Children) != len(n2.Children) {
		t.Fatalf("different child counts: %d vs %d", len(n1.Children), len(n2.Children))
	}
	for i := range n1.Children {
		if n1.Children[i].IntVal != n2.Children[i].IntVal {
			t.Errorf("child %d: %d vs %d", i, n1.Children[i].IntVal, n2.Children[i].IntVal)
		}
	}
}

func TestEdge_CommentsBetweenElements(t *testing.T) {
	n, err := Parse("[1 ; comment\n 2]")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeVector || len(n.Children) != 2 {
		t.Fatalf("expected Vector with 2 children, got %+v", n)
	}
	if n.Children[0].IntVal != 1 || n.Children[1].IntVal != 2 {
		t.Errorf("expected [1, 2], got [%d, %d]", n.Children[0].IntVal, n.Children[1].IntVal)
	}
}

func TestEdge_DeeplyNested(t *testing.T) {
	input := "[[[[42]]]]"
	n, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	// Drill down 4 levels
	for depth := 0; depth < 4; depth++ {
		if n.Type != NodeVector || len(n.Children) != 1 {
			t.Fatalf("depth %d: expected Vector with 1 child, got %+v", depth, n)
		}
		n = n.Children[0]
	}
	if n.Type != NodeInt || n.IntVal != 42 {
		t.Errorf("innermost: expected Int(42), got %+v", n)
	}
}

func TestEdge_MixedNestedCollections(t *testing.T) {
	input := `{:vec [1 2] :list (a b) :set #{:x}}`
	n, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeMap {
		t.Fatalf("expected NodeMap, got %d", n.Type)
	}
	// 6 children: :vec [1 2] :list (a b) :set #{:x}
	if len(n.Children) != 6 {
		t.Fatalf("expected 6 children, got %d", len(n.Children))
	}
	if n.Children[1].Type != NodeVector {
		t.Errorf("child 1: expected Vector, got %d", n.Children[1].Type)
	}
	if n.Children[3].Type != NodeList {
		t.Errorf("child 3: expected List, got %d", n.Children[3].Type)
	}
	if n.Children[5].Type != NodeSet {
		t.Errorf("child 5: expected Set, got %d", n.Children[5].Type)
	}
}

func TestEdge_MapInVector(t *testing.T) {
	n, err := Parse("[{:a 1} {:b 2}]")
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeVector || len(n.Children) != 2 {
		t.Fatalf("expected Vector with 2 maps, got %+v", n)
	}
	for i, child := range n.Children {
		if child.Type != NodeMap {
			t.Errorf("child %d: expected Map, got %d", i, child.Type)
		}
	}
}

func TestEdge_TokenStringer(t *testing.T) {
	// Verify Token.String() doesn't panic and produces expected output
	eof := Token{Type: TokenEOF}
	if eof.String() != "EOF" {
		t.Errorf("EOF.String() = %q", eof.String())
	}
	str := Token{Type: TokenString, Value: "hi"}
	if str.String() != `String("hi")` {
		t.Errorf("String token.String() = %q", str.String())
	}
	atom := Token{Type: TokenAtom, Value: "foo"}
	if atom.String() != "Atom(foo)" {
		t.Errorf("Atom token.String() = %q", atom.String())
	}
}

func TestEdge_MultipleCommentsAndWhitespace(t *testing.T) {
	input := `
	; first comment
	; second comment
	42
	; trailing
	`
	n, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeInt || n.IntVal != 42 {
		t.Errorf("expected Int(42), got %+v", n)
	}
}

func TestEdge_DatalogQuery(t *testing.T) {
	// A realistic Datalog-style query
	input := `[:find ?name ?age
 :where
 [?e :person/name ?name]
 [?e :person/age ?age]]`
	n, err := Parse(input)
	if err != nil {
		t.Fatal(err)
	}
	if n.Type != NodeVector {
		t.Fatalf("expected Vector, got %d", n.Type)
	}
	// :find ?name ?age :where [..] [..]
	if len(n.Children) != 6 {
		t.Fatalf("expected 6 children, got %d", len(n.Children))
	}
	// Verify the where clauses are vectors
	if n.Children[4].Type != NodeVector || n.Children[5].Type != NodeVector {
		t.Error("where clauses should be vectors")
	}
	// Verify variables
	if !n.Children[1].IsVariable() || !n.Children[2].IsVariable() {
		t.Error("?name and ?age should be variables")
	}
}
