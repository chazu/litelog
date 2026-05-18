package edn

import (
	"fmt"
	"strconv"
	"strings"
)

// Parser transforms EDN tokens into an AST.
type Parser struct {
	lexer   *Lexer
	current Token
	peeked  bool
}

// Parse parses a single EDN value from the input string.
func Parse(input string) (Node, error) {
	p := &Parser{lexer: NewLexer(input)}
	node, err := p.readNode()
	if err != nil {
		return Node{}, err
	}
	return node, nil
}

// ParseAll parses all EDN values from the input string until EOF.
func ParseAll(input string) ([]Node, error) {
	p := &Parser{lexer: NewLexer(input)}
	var nodes []Node
	for {
		tok, err := p.peekToken()
		if err != nil {
			return nil, err
		}
		if tok.Type == TokenEOF {
			break
		}
		node, err := p.readNode()
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func (p *Parser) nextToken() (Token, error) {
	if p.peeked {
		p.peeked = false
		return p.current, nil
	}
	tok, err := p.lexer.NextToken()
	if err != nil {
		return Token{}, err
	}
	p.current = tok
	return tok, nil
}

func (p *Parser) peekToken() (Token, error) {
	if p.peeked {
		return p.current, nil
	}
	tok, err := p.lexer.NextToken()
	if err != nil {
		return Token{}, err
	}
	p.current = tok
	p.peeked = true
	return tok, nil
}

func (p *Parser) readNode() (Node, error) {
	tok, err := p.nextToken()
	if err != nil {
		return Node{}, err
	}

	switch tok.Type {
	case TokenEOF:
		return Node{}, fmt.Errorf("unexpected end of input")
	case TokenLParen:
		return p.readList(tok.Line, tok.Col)
	case TokenLBracket:
		return p.readVector(tok.Line, tok.Col)
	case TokenLBrace:
		return p.readMap(tok.Line, tok.Col)
	case TokenHash:
		// Expect { for set literal #{...}
		next, err := p.nextToken()
		if err != nil {
			return Node{}, err
		}
		if next.Type != TokenLBrace {
			return Node{}, fmt.Errorf("line %d col %d: expected '{' after '#', got %s", next.Line, next.Col, next.Value)
		}
		return p.readSet(tok.Line, tok.Col)
	case TokenString:
		return Node{Type: NodeString, Value: tok.Value, Line: tok.Line, Col: tok.Col}, nil
	case TokenAtom:
		return classifyAtom(tok), nil
	case TokenRParen, TokenRBracket, TokenRBrace:
		return Node{}, fmt.Errorf("line %d col %d: unexpected closing delimiter %q", tok.Line, tok.Col, tok.Value)
	default:
		return Node{}, fmt.Errorf("line %d col %d: unexpected token %s", tok.Line, tok.Col, tok.Value)
	}
}

func (p *Parser) readList(line, col int) (Node, error) {
	children, err := p.readUntil(TokenRParen, ")")
	if err != nil {
		return Node{}, err
	}
	return Node{Type: NodeList, Children: children, Line: line, Col: col}, nil
}

func (p *Parser) readVector(line, col int) (Node, error) {
	children, err := p.readUntil(TokenRBracket, "]")
	if err != nil {
		return Node{}, err
	}
	return Node{Type: NodeVector, Children: children, Line: line, Col: col}, nil
}

func (p *Parser) readMap(line, col int) (Node, error) {
	children, err := p.readUntil(TokenRBrace, "}")
	if err != nil {
		return Node{}, err
	}
	if len(children)%2 != 0 {
		return Node{}, fmt.Errorf("line %d col %d: map must contain even number of elements", line, col)
	}
	return Node{Type: NodeMap, Children: children, Line: line, Col: col}, nil
}

func (p *Parser) readSet(line, col int) (Node, error) {
	children, err := p.readUntil(TokenRBrace, "}")
	if err != nil {
		return Node{}, err
	}
	return Node{Type: NodeSet, Children: children, Line: line, Col: col}, nil
}

func (p *Parser) readUntil(closing TokenType, closingName string) ([]Node, error) {
	var children []Node
	for {
		tok, err := p.peekToken()
		if err != nil {
			return nil, err
		}
		if tok.Type == TokenEOF {
			return nil, fmt.Errorf("unexpected end of input, expected %q", closingName)
		}
		if tok.Type == closing {
			p.nextToken() // consume closing delimiter
			return children, nil
		}
		child, err := p.readNode()
		if err != nil {
			return nil, err
		}
		children = append(children, child)
	}
}

// classifyAtom determines the type of an atom token.
func classifyAtom(tok Token) Node {
	v := tok.Value

	switch v {
	case "nil":
		return Node{Type: NodeNil, Value: v, Line: tok.Line, Col: tok.Col}
	case "true":
		return Node{Type: NodeBool, Value: v, Line: tok.Line, Col: tok.Col}
	case "false":
		return Node{Type: NodeBool, Value: v, Line: tok.Line, Col: tok.Col}
	}

	// Keywords start with :
	if strings.HasPrefix(v, ":") {
		return Node{Type: NodeKeyword, Value: v, Line: tok.Line, Col: tok.Col}
	}

	// Try integer
	if i, err := strconv.ParseInt(v, 10, 64); err == nil {
		return Node{Type: NodeInt, Value: v, IntVal: i, Line: tok.Line, Col: tok.Col}
	}

	// Try float
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return Node{Type: NodeFloat, Value: v, FloatVal: f, Line: tok.Line, Col: tok.Col}
	}

	// Everything else is a symbol
	return Node{Type: NodeSymbol, Value: v, Line: tok.Line, Col: tok.Col}
}
