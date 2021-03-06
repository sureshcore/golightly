package golightly

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode"
)

// a map of keywords for quick lookup
var keywords map[string]TokenKind = map[string]TokenKind{
	"break":       TokenKindBreak,
	"case":        TokenKindCase,
	"chan":        TokenKindChan,
	"const":       TokenKindConst,
	"continue":    TokenKindContinue,
	"default":     TokenKindDefault,
	"defer":       TokenKindDefer,
	"else":        TokenKindElse,
	"fallthrough": TokenKindFallthrough,
	"for":         TokenKindFor,
	"func":        TokenKindFunc,
	"go":          TokenKindGo,
	"goto":        TokenKindGoto,
	"if":          TokenKindIf,
	"import":      TokenKindImport,
	"interface":   TokenKindInterface,
	"map":         TokenKindMap,
	"package":     TokenKindPackage,
	"range":       TokenKindRange,
	"return":      TokenKindReturn,
	"select":      TokenKindSelect,
	"struct":      TokenKindStruct,
	"switch":      TokenKindSwitch,
	"type":        TokenKindTypeKeyword,
	"var":         TokenKindVar,

	// pre-declared identifiers - XXX move these to declarations in "universe" later.
	"bool":       TokenKindBool,
	"byte":       TokenKindByte,
	"complex64":  TokenKindComplex64,
	"complex128": TokenKindComplex128,
	"error":      TokenKindError,
	"float32":    TokenKindFloat32,
	"float64":    TokenKindFloat64,
	"int":        TokenKindInt,
	"int8":       TokenKindInt8,
	"int16":      TokenKindInt16,
	"int32":      TokenKindInt32,
	"int64":      TokenKindInt64,
	"rune":       TokenKindRune,
	"string":     TokenKindString,
	"uint":       TokenKindUint,
	"uint8":      TokenKindUint8,
	"uint16":     TokenKindUint16,
	"uint32":     TokenKindUint32,
	"uint64":     TokenKindUint64,
	"uintptr":    TokenKindUintPtr,
	/*
		"true":       TokenKindTrue,
		"false":      TokenKindFalse,
		"iota":       TokenKindIota,
		"nil":        TokenKindNil,
		"append":     TokenKindAppend,
		"cap":        TokenKindCap,
		"close":      TokenKindClose,
		"complex":    TokenKindComplex,
		"copy":       TokenKindCopy,
		"delete":     TokenKindDelete,
		"imag":       TokenKindImag,
		"len":        TokenKindLen,
		"make":       TokenKindMake,
		"new":        TokenKindNew,
		"panic":      TokenKindPanic,
		"print":      TokenKindPrint,
		"println":    TokenKindPrintln,
		"real":       TokenKindReal,
		"recover":    TokenKindRecover,
	*/
}

// the running state of the lexical analyser
type Lexer struct {
	sourceFile string  // name of the source file
	pos        SrcSpan // where we are in the source file

	reader          *bufio.Reader         // used to read the input file
	nextRune        rune                  // the next rune in input
	haveNextRune    bool                  // true if we have a rune buffered in nextRune
	longComment     bool                  // true if we're in a C-style /*...*/ comment
	prevStar        bool                  // true in a long comment if the previous character was an asterisk
	ncNextRunes     [ncNextRunesSize]rune // the next non-comment runes in input
	ncNextRuneCount int                   // count of the number of items in ncNextRunes

	nextTokens     [nextTokensSize]Token // the next tokens
	nextTokenCount int                   // count of the number of items in nextTokens
}

// the buffer size of the lexer output channel
const lexerTokenChannelBuffers = 5
const tokenBufSize = 64
const ncNextRunesSize = 3
const nextTokensSize = 2
const initialStringStorage = 80

// NewLexer creates a new lexer object
func NewLexer() *Lexer {
	l := new(Lexer)
	l.Init("-")
	return l
}

// Init initialises the lexer before using LexLine.
func (l *Lexer) Init(filename string) {
	l.pos = SrcSpan{SrcLoc{1, 1}, SrcLoc{1, 1}}
	l.sourceFile = filename
	l.nextTokenCount = 0
	l.haveNextRune = false
	l.ncNextRuneCount = 0
	l.longComment = false
}

func (l *Lexer) Close() {
}

// LexReader starts lexical analysis of a generalised Reader.
// It creates its own buffering of the reader, so it's not necessary to
// provide a buffered reader.
func (l *Lexer) LexReader(r io.Reader, filename string) {
	// start afresh
	l.Init(filename)
	l.reader = bufio.NewReader(r)
}

// getBufferedRune gets a rune from the source including comments etc..
// it's designed to be called from getUntrackedRune() only.
func (l *Lexer) getBufferedRune() (rune, error) {
	if l.haveNextRune {
		// get it from our buffer
		l.haveNextRune = false
		return l.nextRune, nil
	} else {
		// read it
		r, _, err := l.reader.ReadRune()
		return r, err
	}
}

// getUntrackedRune gets a rune while removing comments from the stream.
// it doesn't change the line/column tracking.
func (l *Lexer) getUntrackedRune() (rune, error) {
	// do we have a buffered rune with comments already removed?
	if l.ncNextRuneCount > 0 {
		// get it from the nc (non-commented) buffer
		r := l.ncNextRunes[0]

		// remove it from the buffer
		for i := l.ncNextRuneCount - 1; i > 0; i-- {
			l.ncNextRunes[i-1] = l.ncNextRunes[i]
		}
		l.ncNextRuneCount--

		return r, nil
	}

	// get a rune
	r, err := l.getBufferedRune()
	if err != nil {
		return 0, err
	}

	// are we in a C-style /*...*/ comment?
	if !l.longComment {
		// no, check if a comment is starting
		if r == '/' {
			// this might be the start of a comment
			r2, err2 := l.getBufferedRune()
			if err2 != nil {
				if err2 == io.EOF {
					// it was a slash at EOF. just return it.
					return r, nil
				} else {
					return 0, err2
				}
			}

			switch r2 {
			case '/':
				// comment until end of line, absorb the rest of the line
				for {
					r, err = l.getBufferedRune()
					if err != nil {
						return 0, err
					}

					if r == '\n' {
						// return end of line
						return r, nil
					}
				}

			case '*':
				// C-style /*...*/ comment starts here. return spaces for
				// these characters so column counts work correctly.
				l.haveNextRune = true
				l.nextRune = ' '
				l.longComment = true
				l.prevStar = false
				return ' ', nil

			default:
				// it's not a comment at all. return it as normal.
				l.haveNextRune = true
				l.nextRune = r2
				return r, nil
			}
		}
	} else {
		// we're in a C-style /*...*/ comment. return line feeds and convert
		// everything else into spaces so column counts work correctly.
		switch r {
		case '\n':
			// end of line - return is so we can count lines.
			l.prevStar = false
			return r, nil

		case '*':
			// possible end of comment coming up.
			l.prevStar = true
			return ' ', nil

		case '/':
			if l.prevStar {
				// end of comment.
				l.longComment = false
			}
			return ' ', nil

		default:
			// any other comment character is just converted to a space.
			l.prevStar = false
			return ' ', nil
		}
	}

	// just a normal character
	return r, nil
}

// peekRune returns a rune from ahead while removing comments from the stream.
// it doesn't change the line/column tracking.
func (l *Lexer) peekRune(ahead int) (rune, error) {
	// make sure the buffer is full enough
	for l.ncNextRuneCount <= ahead {
		// get a character
		r, err := l.getRune()
		if err != nil {
			return 0, err
		}

		// buffer it
		l.ncNextRunes[l.ncNextRuneCount] = r
		l.ncNextRuneCount++
	}

	// return it
	return l.ncNextRunes[ahead], nil
}

// getRune gets a rune while removing comments from the stream and tracking
// line/column counts.
func (l *Lexer) getRune() (rune, error) {
	// get the next character
	ch, err := l.getUntrackedRune()
	if err != nil {
		return 0, err
	}

	// count columns and lines
	if ch == '\n' {
		l.pos.end.Line++
		l.pos.end.Column = 1
	} else {
		l.pos.end.Column++
	}

	return ch, nil
}

// tossRunes throws away a number of runes (which we've probably already
// scanned using peekRune). it also tracks line/column counts.
func (l *Lexer) tossRunes(howMany int) error {
	for i := 0; i < howMany; i++ {
		_, err := l.getRune()
		if err != nil {
			return err
		}
	}

	return nil
}

// skipWhitespace gets a rune while skipping whitespace and keeping
// track of column and line counts.
func (l *Lexer) skipWhitespace() error {
	// skip leading whitespace
	for {
		ch, err := l.peekRune(0)
		if err != nil {
			if err == io.EOF {
				// end of source
				return nil
			} else {
				return err
			}
		}

		// is it whitespace?
		if ch != ' ' && ch != '\t' && ch != '\r' && ch != '\n' {
			// no, return
			return nil
		}

		// move to the next character
		l.getRune()
	}
}

// GetToken gets the next token from the buffer.
// returns the token and an error.
func (l *Lexer) GetToken() (Token, error) {
	// do we have a buffered token?
	if l.nextTokenCount > 0 {
		// get it from the buffer
		t := l.nextTokens[0]

		// remove it from the buffer
		for i := l.nextTokenCount - 1; i > 0; i-- {
			l.nextTokens[i-1] = l.nextTokens[i]
		}
		l.nextTokens[l.nextTokenCount-1] = nil
		l.nextTokenCount--

		return t, nil
	}

	return l.lexToken()
}

// PeekToken returns the next token from the line buffer without removing it.
// returns the token and an error.
func (l *Lexer) PeekToken(ahead int) (Token, error) {
	// make sure the nextTokens buffer is full enough
	for l.nextTokenCount <= ahead {
		// get a token
		t, err := l.lexToken()
		if err != nil {
			return nil, err
		}

		// buffer it
		l.nextTokens[l.nextTokenCount] = t
		l.nextTokenCount++
	}

	// return it
	return l.nextTokens[ahead], nil
}

// lexToken gets the next token from the line buffer.
// adds the token to the token list.
// returns success and an error. success is false at end of line.
func (l *Lexer) lexToken() (Token, error) {
	// get a character
	err := l.skipWhitespace()
	if err != nil {
		return nil, err
	}

	l.pos.start = l.pos.end

	// get the next character
	ch, err := l.peekRune(0)
	if err != nil {
		return nil, err
	}

	// is it an identifier?
	if unicode.IsLetter(ch) || ch == '_' {
		// get the word
		word := l.getWord()

		// is it a keyword?
		token, ok := keywords[word]
		if ok {
			return SimpleToken{l.pos, token}, nil
		}

		// it must be an identifier
		return StringToken{SimpleToken{l.pos, TokenKindIdentifier}, word}, nil
	}

	// is it a numeric literal?
	if unicode.IsDigit(ch) {
		// starts with a digit
		return l.getNumeric()
	} else if ch == '.' {
		// starts with '.', is it followed by a digit?
		ch2, _ := l.peekRune(1)
		if unicode.IsDigit(ch2) {
			// of the form '.4356'
			return l.getNumeric()
		}
	}

	// is it an operator?
	token, runes, isOp := l.getOperator(ch)
	if isOp {
		l.tossRunes(runes)
		return SimpleToken{l.pos, token}, nil
	}

	// is it a string literal?
	switch ch {
	case '\'':
		return l.getRuneLiteral()

	case '"', '`':
		return l.getStringLiteral()
	}

	return nil, errors.New(fmt.Sprintf("illegal character '%c' (0x%02x)", ch, ch))
}

// getOperator gets an operator token.
// returns the token, the number of characters absorbed and success.
func (l *Lexer) getOperator(ch rune) (TokenKind, int, bool) {
	// operator lexing is performed as a hard-coded trie for speed.
	switch ch {
	case '+':
		ch2, _ := l.peekRune(1)
		switch ch2 {
		case '=': // '+='
			return TokenKindAddAssign, 2, true
		case '+': // '++'
			return TokenKindIncrement, 2, true
		default: // '+'
			return TokenKindAdd, 1, true
		}

	case '-':
		ch2, _ := l.peekRune(1)
		switch ch2 {
		case '=': // '-='
			return TokenKindSubtractAssign, 2, true
		case '-': // '--'
			return TokenKindDecrement, 2, true
		default: // '-'
			return TokenKindSubtract, 1, true
		}

	case '*':
		ch2, _ := l.peekRune(1)
		if ch2 == '=' { // '*='
			return TokenKindMultiplyAssign, 2, true
		} else { // '*'
			return TokenKindAsterisk, 1, true
		}

	case '/':
		ch2, _ := l.peekRune(1)
		if ch2 == '=' { // '/='
			return TokenKindDivideAssign, 2, true
		} else { // '/'
			return TokenKindDivide, 1, true
		}

	case '%':
		ch2, _ := l.peekRune(1)
		if ch2 == '=' { // '%='
			return TokenKindModulusAssign, 2, true
		} else { // '%'
			return TokenKindModulus, 1, true
		}

	case '&':
		ch2, _ := l.peekRune(1)
		switch ch2 {
		case '=': // '&='
			return TokenKindBitwiseAndAssign, 2, true
		case '&': // '&&'
			return TokenKindLogicalAnd, 2, true
		default: // '&'
			return TokenKindBitwiseAnd, 1, true
		}

	case '|':
		ch2, _ := l.peekRune(1)
		switch ch2 {
		case '=': // '|='
			return TokenKindBitwiseOrAssign, 2, true
		case '|': // '||'
			return TokenKindLogicalOr, 2, true
		default: // '|'
			return TokenKindBitwiseOr, 1, true
		}

	case '^':
		ch2, _ := l.peekRune(1)
		if ch2 == '=' { // '^='
			return TokenKindBitwiseExorAssign, 2, true
		} else { // '^'
			return TokenKindBitwiseExor, 1, true
		}

	case '<':
		ch2, _ := l.peekRune(1)
		switch ch2 {
		case '<':
			// look ahead another character
			ch3, _ := l.peekRune(2)
			if ch3 == '=' { // '<<='
				return TokenKindShiftLeftAssign, 3, true
			} else { // '<<'
				return TokenKindShiftLeft, 2, true
			}
		case '=': // '<='
			return TokenKindLessEqual, 2, true
		case '-': // '<-'
			return TokenKindChannelArrow, 2, true
		default: // '<'
			return TokenKindLess, 1, true
		}

	case '>':
		ch2, _ := l.peekRune(1)
		switch ch2 {
		case '>':
			// look ahead another character
			ch3, _ := l.peekRune(2)
			if ch3 == '=' { // '>>='
				return TokenKindShiftRightAssign, 3, true
			} else { // '>>'
				return TokenKindShiftRight, 2, true
			}
		case '=': // '>='
			return TokenKindGreaterEqual, 2, true
		default: // '>'
			return TokenKindGreater, 1, true
		}

	case '=':
		ch2, _ := l.peekRune(1)
		if ch2 == '=' { // '=='
			return TokenKindEquals, 2, true
		} else { // '='
			return TokenKindAssign, 1, true
		}

	case '!':
		ch2, _ := l.peekRune(1)
		if ch2 == '=' { // '!='
			return TokenKindNotEqual, 2, true
		} else { // '!'
			return TokenKindNot, 1, true
		}

	case ':':
		ch2, _ := l.peekRune(1)
		if ch2 == '=' { // ':='
			return TokenKindDeclareAssign, 2, true
		} else { // ':'
			return TokenKindColon, 1, true
		}

	case '.': // '.'
		return TokenKindDot, 1, true
	case ',': // ','
		return TokenKindComma, 1, true
	case '(': // '('
		return TokenKindOpenBracket, 1, true
	case ')': // ')'
		return TokenKindCloseBracket, 1, true
	case '[': // '['
		return TokenKindOpenSquareBracket, 1, true
	case ']': // ']'
		return TokenKindCloseSquareBracket, 1, true
	case '{': // '{'
		return TokenKindOpenBrace, 1, true
	case '}': // '}'
		return TokenKindCloseBrace, 1, true
	case ';': // ';'
		return TokenKindSemicolon, 1, true
	}

	return 0, 0, false
}

// getWord gets an identifier. returns the word.
func (l *Lexer) getWord() string {
	// get characters until the end
	var word string
	for {
		// get the next rune
		ch, err := l.peekRune(0)
		if err != nil {
			return word
		}

		// done at end of word
		if !unicode.IsLetter(ch) && ch != '_' {
			return word
		}

		// add the character to our word and move to the next character
		word += string(ch)
		l.getRune()
	}
}

// getNumeric gets a number.
// XXX - this is currently a quickie version. This should be reimplemented fully according to spec later.
func (l *Lexer) getNumeric() (Token, error) {
	// get characters until the end
	var word string
	var isFloat bool
	for {
		// get the next rune
		ch, err := l.peekRune(0)
		if err != nil {
			break
		}

		// done at end of word
		if !unicode.IsDigit(ch) && ch != '.' && ch != 'e' {
			break
		}

		// take note if it looks like a float
		if ch == '.' || ch == 'e' {
			isFloat = true
		}

		// add the character to our word and move to the next character
		word += string(ch)
		l.getRune()
	}

	// is the next character a "." or "e"? If so, it's a float.
	if isFloat {
		// parse the float
		v, err := strconv.ParseFloat(word, 128)
		if err != nil {
			return nil, NewError(l.sourceFile, l.pos, err.Error())
		}

		return FloatToken{SimpleToken{l.pos, TokenKindLiteralFloat}, v}, nil
	} else {
		// it's an int, parse it
		v, err := strconv.ParseUint(word, 10, 64)
		if err != nil {
			return nil, NewError(l.sourceFile, l.pos, err.Error())
		}

		return UintToken{SimpleToken{l.pos, TokenKindLiteralInt}, v}, nil
	}
}

// getRuneLiteral gets a single character rune literal.
func (l *Lexer) getRuneLiteral() (Token, error) {
	// get it as a string literal
	str, err := l.getStringLiteralSimple()
	if err != nil {
		return nil, err
	}

	if len(str) != 1 {
		return nil, NewError(l.sourceFile, l.pos, "this rune should be a single character")
	}

	return UintToken{SimpleToken{l.pos, TokenKindLiteralRune}, uint64(str[0])}, nil
}

// getStringLiteral gets a string literal.
func (l *Lexer) getStringLiteral() (Token, error) {
	// get the string literal
	str, err := l.getStringLiteralSimple()
	if err != nil {
		return nil, err
	}

	// we're at the end of the string
	return StringToken{SimpleToken{l.pos, TokenKindLiteralString}, string(str)}, nil
}

// getStringLiteralSimple gets a string literal, returning it as a []rune.
// XXX - this is currently a quickie version. This should be reimplemented fully according to spec later.
func (l *Lexer) getStringLiteralSimple() ([]rune, error) {
	// get the open quote
	quote, _ := l.getRune()

	// get characters until we find the closing quote
	str := make([]rune, 0, initialStringStorage)
	for {
		ch, err := l.getRune()
		if err != nil {
			// just return what we've got
			return nil, NewError(l.sourceFile, l.pos, "no closing quote")
		}

		if ch == quote {
			// we're at the end of the string
			return str, nil
		}

		// put it in the string
		str = append(str, ch)
	}
}
