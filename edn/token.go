package edn

import "fmt"

// TokenType identifies the kind of lexical token.
type TokenType int

const (
	TokenEOF      TokenType = iota
	TokenString             // "hello"
	TokenAtom               // symbols, keywords, numbers, booleans, nil
	TokenLParen             // (
	TokenRParen             // )
	TokenLBracket           // [
	TokenRBracket           // ]
	TokenLBrace             // {
	TokenRBrace             // }
	TokenHash               // # (for sets #{})
)

// Token is a single lexical unit produced by the lexer.
type Token struct {
	Type  TokenType
	Value string
	Line  int
	Col   int
}

func (t Token) String() string {
	switch t.Type {
	case TokenEOF:
		return "EOF"
	case TokenString:
		return fmt.Sprintf("String(%q)", t.Value)
	case TokenAtom:
		return fmt.Sprintf("Atom(%s)", t.Value)
	default:
		return fmt.Sprintf("Token(%d, %q)", t.Type, t.Value)
	}
}
