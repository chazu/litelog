package edn

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// Lexer tokenizes EDN input.
type Lexer struct {
	input []byte
	pos   int
	line  int
	col   int
}

// NewLexer creates a lexer for the given input.
func NewLexer(input string) *Lexer {
	return &Lexer{
		input: []byte(input),
		pos:   0,
		line:  1,
		col:   1,
	}
}

func (l *Lexer) peek() (rune, int) {
	if l.pos >= len(l.input) {
		return 0, 0
	}
	return utf8.DecodeRune(l.input[l.pos:])
}

func (l *Lexer) advance() rune {
	r, size := utf8.DecodeRune(l.input[l.pos:])
	l.pos += size
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

func (l *Lexer) skipWhitespaceAndComments() {
	for l.pos < len(l.input) {
		r, _ := l.peek()
		// Commas are whitespace in EDN
		if unicode.IsSpace(r) || r == ',' {
			l.advance()
			continue
		}
		// Line comments
		if r == ';' {
			for l.pos < len(l.input) {
				r, _ = l.peek()
				if r == '\n' {
					break
				}
				l.advance()
			}
			continue
		}
		break
	}
}

func isDelimiter(r rune) bool {
	switch r {
	case '(', ')', '[', ']', '{', '}', '"', ';':
		return true
	}
	return unicode.IsSpace(r) || r == ','
}

// NextToken returns the next token from the input.
func (l *Lexer) NextToken() (Token, error) {
	l.skipWhitespaceAndComments()

	if l.pos >= len(l.input) {
		return Token{Type: TokenEOF, Line: l.line, Col: l.col}, nil
	}

	line, col := l.line, l.col
	r, _ := l.peek()

	switch r {
	case '(':
		l.advance()
		return Token{Type: TokenLParen, Value: "(", Line: line, Col: col}, nil
	case ')':
		l.advance()
		return Token{Type: TokenRParen, Value: ")", Line: line, Col: col}, nil
	case '[':
		l.advance()
		return Token{Type: TokenLBracket, Value: "[", Line: line, Col: col}, nil
	case ']':
		l.advance()
		return Token{Type: TokenRBracket, Value: "]", Line: line, Col: col}, nil
	case '{':
		l.advance()
		return Token{Type: TokenLBrace, Value: "{", Line: line, Col: col}, nil
	case '}':
		l.advance()
		return Token{Type: TokenRBrace, Value: "}", Line: line, Col: col}, nil
	case '#':
		l.advance()
		return Token{Type: TokenHash, Value: "#", Line: line, Col: col}, nil
	case '"':
		return l.readString(line, col)
	default:
		return l.readAtom(line, col)
	}
}

func (l *Lexer) readString(line, col int) (Token, error) {
	l.advance() // consume opening "
	var buf []byte
	for {
		if l.pos >= len(l.input) {
			return Token{}, fmt.Errorf("line %d col %d: unterminated string", line, col)
		}
		r, _ := l.peek()
		if r == '"' {
			l.advance()
			return Token{Type: TokenString, Value: string(buf), Line: line, Col: col}, nil
		}
		if r == '\\' {
			l.advance()
			if l.pos >= len(l.input) {
				return Token{}, fmt.Errorf("line %d col %d: unterminated string escape", line, col)
			}
			esc, _ := l.peek()
			l.advance()
			switch esc {
			case '"':
				buf = append(buf, '"')
			case '\\':
				buf = append(buf, '\\')
			case 'n':
				buf = append(buf, '\n')
			case 't':
				buf = append(buf, '\t')
			case 'r':
				buf = append(buf, '\r')
			default:
				buf = append(buf, '\\')
				buf = utf8.AppendRune(buf, esc)
			}
			continue
		}
		l.advance()
		buf = utf8.AppendRune(buf, r)
	}
}

func (l *Lexer) readAtom(line, col int) (Token, error) {
	start := l.pos
	for l.pos < len(l.input) {
		r, _ := l.peek()
		if isDelimiter(r) || r == '#' {
			break
		}
		l.advance()
	}
	value := string(l.input[start:l.pos])
	if value == "" {
		r, _ := l.peek()
		return Token{}, fmt.Errorf("line %d col %d: unexpected character %q", line, col, r)
	}
	return Token{Type: TokenAtom, Value: value, Line: line, Col: col}, nil
}
