package golightly

import (
	"fmt"
)

// type Parser controls parsing of a token stream into an AST.
type Parser struct {
	lexer         *Lexer         // the lexical analyser.
	ts            *DataTypeStore // the data type store.
	sf            *sourceFile    // handy info about this source file.

	filename    string // the name of the file being parsed.
	packageName string // the name of the package this file is a part of.
}

// NewParser creates a new parser object.
func NewParser(lexer *Lexer, ts *DataTypeStore, sf *sourceFile) *Parser {
	p := new(Parser)
	p.lexer = lexer
	p.ts = ts
	p.sf = sf

	return p
}

// Parse runs the parser and breaks the program down into an Abstract Syntax Tree.
func (p *Parser) Parse() error {
	return p.parseSourceFile()
}

// parseSourceFile parses the contents of an entire source file.
// SourceFile       = PackageClause ";" { ImportDecl ";" } { TopLevelDecl ";" } .
func (p *Parser) parseSourceFile() error {
	// get the package declaration.
	ast := new(ASTTopLevel)
	packageName, err := p.parsePackage()
	if err != nil {
		return err
	}
	ast.packageName = packageName

	// get a semicolon separator.
	err = p.expectToken(TokenKindSemicolon, "I'm gonna be needing a semicolon after this 'package' declaration")
	if err != nil {
		return err
	}

	// get a number of import declarations.
	tok, err := p.lexer.PeekToken(0)
	if err != nil {
		return err
	}

	if tok.TokenKind() == TokenKindImport {
		for {
			// get an import.
			imports, err := p.parseImport()
			if err != nil {
				return err
			}

			ast.imports = append(ast.imports, imports...)

			// get a semicolon separator.
			err = p.expectToken(TokenKindSemicolon, "I'm gonna be needing a semicolon after this 'import' declaration")
			if err != nil {
				return err
			}
		}
	}

	// get a number of top-level declarations.
	tok, err = p.lexer.PeekToken(0)
	if err != nil {
		return err
	}

	for {
		// get a top-level declaration.
		match, topLevelDecls, err := p.parseTopLevelDecl()
		if err != nil {
			return err
		}

		if !match {
			break
		}

		ast.topLevelDecls = append(ast.topLevelDecls, topLevelDecls...)

		// get a semicolon separator.
		err = p.expectToken(TokenKindSemicolon, "I need a semicolon here")
		if err != nil {
			return err
		}
	}

	// make sure we're at the end of the file.
	err = p.expectToken(TokenKindEndOfSource, "I don't really know what this is or why it's here")
	if err != nil {
		return err
	}

	return nil
}

// parsePackage parses a package declaration.
// PackageClause  = "package" PackageName .
func (p *Parser) parsePackage() (string, error) {
	// get the package declaration
	err := p.expectToken(TokenKindPackage, "the file should start with 'package <package name>'")
	if err != nil {
		return "", err
	}

	packageNameToken, err := p.lexer.GetToken()
	if err != nil {
		return "", err
	}
	if packageNameToken.TokenKind() != TokenKindIdentifier {
		return "", NewError(p.filename, packageNameToken.Pos(), "the package name should be a plain word. eg. 'package horatio'")
	}

	strPackageName := packageNameToken.(StringToken)

	return strPackageName.strVal, nil
}

// parseImport parses an import declaration.
// ImportDecl       = "import" ( ImportSpec | "(" { ImportSpec ";" } ")" ) .
func (p *Parser) parseImport() ([]AST, error) {
	// get the import declaration
	importToken, err := p.lexer.PeekToken(0)
	if err != nil {
		return nil, err
	}
	if importToken.TokenKind() != TokenKindImport {
		return nil, nil
	}

	// is it a group or a single import?
	p.lexer.GetToken()
	nextToken, err := p.lexer.PeekToken(0)
	if err != nil {
		return nil, err
	}
	if nextToken.TokenKind() == TokenKindOpenBracket {
		// get a series of import specs.
		imports, err := p.parseGroupSingle(p.parseImportSpec, "import")
		if err != nil {
			return nil, err
		}

		return imports, nil
	} else {
		// get a single import.
		tree, err := p.parseImportSpec()
		if err != nil {
			return nil, err
		}

		astSlice := make([]AST, 1)
		astSlice[0] = tree
		return astSlice, nil
	}
}

// parseImportSpec parses import specifications as part of an import statement.
// ImportSpec       = [ "." | PackageName ] ImportPath .
func (p *Parser) parseImportSpec() (AST, error) {
	// what kind of thing are we looking at?
	nextToken, err := p.lexer.PeekToken(0)
	if err != nil {
		return nil, err
	}

	switch nextToken.TokenKind() {
	case TokenKindIdentifier:
		// it's of the form 'import fred "frod"' - get a package name first.
		strPackageName := nextToken.(StringToken)
		p.lexer.GetToken()

		// get an import path.
		pathToken, err := p.lexer.GetToken()
		if err != nil {
			return nil, err
		}
		if pathToken.TokenKind() != TokenKindString {
			return nil, NewError(p.filename, pathToken.Pos(), "this should have been a string. eg. 'import fred \"github.com/fred/thefredpackage\"'")
		}

		// tell the compiler to read the imported file
		p.sf.addImport <- importMessage{pathToken.(StringToken).strVal, p.filename, pathToken.Pos(), nil} // XXX - need to give a completion channel.

		// return the import spec
		return ASTImport{pathToken.Pos(), ASTIdentifier{nextToken.Pos(), "", strPackageName.strVal}, NewASTValueFromToken(pathToken, p.ts)}, nil

	case TokenKindString:
		// it's of the form 'import "frod"' - just get the import path.
		p.lexer.GetToken()

		// tell the compiler to read the imported file
		p.sf.addImport <- importMessage{nextToken.(StringToken).strVal, p.filename, nextToken.Pos(), nil} // XXX - need to give a completion channel.

		// return the import spec
		return ASTImport{nextToken.Pos(), nil, NewASTValueFromToken(nextToken, p.ts)}, nil

	default:
		return nil, NewError(p.filename, nextToken.Pos(), "this import makes no sense. It should be like 'import [cool] \"coolpackage\"'")
	}
}

// parseTopLevelDecl parses a top-level declaration.
// TopLevelDecl  = Declaration | FunctionDecl | MethodDecl .
// Declaration   = ConstDecl | TypeDecl | VarDecl .
func (p *Parser) parseTopLevelDecl() (bool, []AST, error) {
	// what kind of thing are we looking at?
	nextToken, err := p.lexer.PeekToken(0)
	if err != nil {
		return false, nil, err
	}

	switch nextToken.TokenKind() {
	case TokenKindConst:
		asts, err := p.parseDecl(p.parseConstSpec, "const")
		return true, asts, err

	case TokenKindTypeKeyword:
		asts, err := p.parseDecl(p.parseTypeSpec, "type")
		return true, asts, err

	case TokenKindVar:
		asts, err := p.parseDecl(p.parseVarSpec, "var")
		return true, asts, err

	case TokenKindFunc:
		// it's a func or method decl.
		ast, err := p.parseFunctionDecl()
		return true, []AST{ast}, err

	default:
		return false, nil, NewError(p.filename, nextToken.Pos(), "so I wanted a top level thing like a type, a func, a const or a var, but no... you had to be different")
	}
}

// parseDecl parses a declaration. It's used for const, type and var
// declarations since they're all fairly similar.
// ConstDecl      = "const" ( ConstSpec | "(" { ConstSpec ";" } ")" ) .
// TypeDecl       = "type"  ( TypeSpec  | "(" { TypeSpec  ";" } ")" ) .
// VarDecl        = "var"   ( VarSpec   | "(" { VarSpec   ";" } ")" ) .
func (p *Parser) parseDecl(parseSpec func() ([]AST, error), verbName string) ([]AST, error) {
	// we already know it starts with the verb, so skip that
	p.lexer.GetToken()

	// is it a '(' next?
	bracketToken, err := p.lexer.PeekToken(0)
	if err != nil {
		return nil, err
	}

	var decls []AST
	if bracketToken.TokenKind() == TokenKindOpenBracket {
		// it's a group of specs.
		decls, err = p.parseGroupMulti(parseSpec, verbName)
		if err != nil {
			return nil, err
		}
	} else {
		// it's a single spec.
		decls, err = parseSpec()
		if err != nil {
			return nil, err
		}
	}

	return decls, nil
}

// parseConstSpec parses a constant spec.
// ConstSpec      = IdentifierList [ [ Type ] "=" ExpressionList ] .
func (p *Parser) parseConstSpec() ([]AST, error) {
	// get the identifier list
	identList, err := p.parseIdentifierList("constant")
	if err != nil {
		return nil, err
	}

	// is there a data type following?
	matchTyp, typeAST, err := p.parseDataType()
	if err != nil {
		return nil, err
	}

	// maybe an equals?
	equalsToken, err := p.lexer.PeekToken(0)
	if err != nil {
		return nil, err
	}

	// handle optional part.
	var exprList []AST
	if matchTyp || equalsToken.TokenKind() == TokenKindEquals {
		// there must be an '=' and expression list after a type.
		if equalsToken.TokenKind() != TokenKindEquals {
			return nil, NewError(p.filename, equalsToken.Pos(), "after a data type I expected to see '=' here")
		}

		// get the expression list.
		p.lexer.GetToken()
		exprList, err = p.parseExpressionList()
		if err != nil {
			return nil, err
		}
	}

	// are the two lists the same length?
	identSpan := identList[0].Pos().Add(identList[len(identList)-1].Pos())
	if len(identList) > len(exprList) {
		return nil, NewError(p.filename, identSpan, "there are more names here than there are values")
	} else if len(identList) < len(exprList) {
		return nil, NewError(p.filename, identSpan, "there are less names here than there are values")
	}

	// make a set of consts out of all this.
	asts := make([]AST, len(identList))
	for i := 0; i < len(identList); i++ {
		asts[i] = ASTConstDecl{identList[i], typeAST, exprList[i]}
	}

	return asts, nil
}

// parseTypeSpec parses a type declaration specification.
// TypeSpec     = identifier Type .
func (p *Parser) parseTypeSpec() ([]AST, error) {
	// get an identifier
	ident, err := p.lexer.GetToken()
	if err != nil {
		return nil, err
	}

	if ident.TokenKind() != TokenKindIdentifier {
		return nil, NewError(p.filename, ident.Pos(), fmt.Sprint("this should have been a name for a type, but it's not"))
	}

	identAST := ASTIdentifier{ident.Pos(), "", ident.(StringToken).strVal}

	// get the data type
	matchTyp, typeAST, err := p.parseDataType()
	if err != nil {
		return nil, err
	}

	// the type is mandatory here.
	if !matchTyp {
		fail, err := p.lexer.PeekToken(0)
		if err != nil {
			return nil, err
		}

		return nil, NewError(p.filename, fail.Pos(), fmt.Sprint("this should have been a name for a type, but it's not"))
	}

	return []AST{ASTDataTypeDecl{identAST, typeAST}}, nil
}

// parseVarSpec parses a variable declaration specification.
// VarSpec     = IdentifierList ( Type [ "=" ExpressionList ] | "=" ExpressionList ) .
func (p *Parser) parseVarSpec() ([]AST, error) {
	// get the identifier list
	identList, err := p.parseIdentifierList("variable")
	if err != nil {
		return nil, err
	}

	// is there a data type following?
	matchTyp, typeAST, err := p.parseDataType()
	if err != nil {
		return nil, err
	}

	var exprList []AST
	if matchTyp {
		// optional equals.
		equalsToken, err := p.lexer.PeekToken(0)
		if err != nil {
			return nil, err
		}

		if equalsToken.TokenKind() == TokenKindEquals {
			// get the expression list.
			p.lexer.GetToken()
			exprList, err = p.parseExpressionList()
			if err != nil {
				return nil, err
			}
		}
	} else {
		// required equals.
		err := p.expectToken(TokenKindEquals, "I was expecting to see an '=' here")
		if err != nil {
			return nil, err
		}

		// get the expression list.
		p.lexer.GetToken()
		exprList, err = p.parseExpressionList()
		if err != nil {
			return nil, err
		}
	}

	// are the two lists the same length?
	if exprList != nil {
		identSpan := identList[0].Pos().Add(identList[len(identList)-1].Pos())

		if len(identList) > len(exprList) {
			return nil, NewError(p.filename, identSpan, "there are more names here than there are values")
		} else if len(identList) < len(exprList) {
			return nil, NewError(p.filename, identSpan, "there are less names here than there are values")
		}
	}

	// make a set of variable declarations out of all this.
	asts := make([]AST, len(identList))
	for i := 0; i < len(identList); i++ {
		asts[i] = ASTVarDecl{identList[i], typeAST, exprList[i]}
	}

	return asts, nil
}

// parseIdentifierList parses a comma-separated list of identifiers.
// IdentifierList = identifier { "," identifier } .
func (p *Parser) parseIdentifierList(identDesc string) ([]AST, error) {
	var asts []AST

	for {
		// get an identifier.
		ident, err := p.lexer.GetToken()
		if err != nil {
			return nil, err
		}

		if ident.TokenKind() != TokenKindIdentifier {
			return nil, NewError(p.filename, ident.Pos(), fmt.Sprint("this should have been a name for a ", identDesc, ", but it's not"))
		}

		// add the identifier to our list of identifiers.
		asts = append(asts, ASTIdentifier{ident.Pos(), "", ident.(StringToken).strVal})

		// look for a comma after it.
		comma, err := p.lexer.PeekToken(0)
		if err != nil {
			return nil, err
		}

		if comma.TokenKind() != TokenKindComma {
			break
		}

		p.lexer.GetToken()
	}

	return asts, nil
}

// parseFunctionDecl parses a function or method declaration. Note that
// "func" will already have been consumed so we're starting from the
// FunctionName or receiver.
// FunctionDecl = "func" FunctionName ( Function | Signature ) .
func (p *Parser) parseFunctionDecl() (AST, error) {
	// we already know it starts with "func"
	funcToken, _ := p.lexer.GetToken()

	// get an identifier for the function name or possibly a receiver.
	tok, err := p.lexer.PeekToken(0)
	if err != nil {
		return nil, err
	}

	var receiver AST
	if tok.TokenKind() == TokenKindOpenBracket {
		// it's a receiver.
		receiver, err = p.parseReceiver()

		// take a look at the next token.
		tok, err = p.lexer.PeekToken(0)
		if err != nil {
			return nil, err
		}
	}

	if tok.TokenKind() != TokenKindIdentifier {
		return nil, NewError(p.filename, tok.Pos(), fmt.Sprint("this should have been a function name, but it's not"))
	}
	funcName := tok.(StringToken).strVal
	p.lexer.GetToken()

	// get a signature.
	params, returns, err := p.parseSignature()
	if err != nil {
		return nil, err
	}

	// this might be followed by a function body.
	bodyToken, err := p.lexer.PeekToken(0)
	var body AST
	if bodyToken.TokenKind() == TokenKindOpenBrace {
		// parse a function body.
		body, err = p.parseBlock()
		if err != nil {
			return nil, err
		}
	}

	return ASTFunctionDecl{funcToken.Pos().Add(tok.Pos()), funcName, receiver, params, returns, body}, nil
}

// parseReceiver parses a method receiver.
// Receiver     = "(" [ identifier ] [ "*" ] BaseTypeName ")" .
// BaseTypeName = identifier .
func (p *Parser) parseReceiver() (AST, error) {
	// get the opening bracket
	bracketPos, err := p.expectTokenPos(TokenKindOpenBracket, "receivers start with an open bracket, but that's not what I'm seeing")
	if err != nil {
		return nil, err
	}

	// get an optional identifier.
	var ident string
	tok, err := p.lexer.GetToken()
	if err != nil {
		return nil, err
	}
	tok2, err := p.lexer.PeekToken(1)
	if err != nil {
		return nil, err
	}

	if tok.TokenKind() == TokenKindIdentifier && tok2.TokenKind() != TokenKindCloseBracket {
		ident = tok.(StringToken).strVal

		// get the next token.
		tok, err = p.lexer.GetToken()
		if err != nil {
			return nil, err
		}
	}

	// get an optional '*'.
	pointer := false
	if tok.TokenKind() == TokenKindAsterisk {
		pointer = true

		// get the next token.
		tok, err = p.lexer.GetToken()
		if err != nil {
			return nil, err
		}
	}

	// get the base type name.
	if tok.TokenKind() != TokenKindIdentifier {
		return nil, NewError(p.filename, tok.Pos(), "I was expecting a type name in this receiver. Receivers should look like '(rec_var [*]type_name)'")
	}
	baseTypeName := tok.(StringToken).strVal

	// now get the closing bracket.
	endBracketPos, err := p.expectTokenPos(TokenKindCloseBracket, "I'd like a ')' to finish this receiver... thanks")

	return ASTReceiver{bracketPos.Add(endBracketPos), ident, pointer, baseTypeName}, nil
}

// parseGroupSingle parses a group of some other clause, surrounded by brackets and
// with semicolons after each entry.
func (p *Parser) parseGroupSingle(parseClause func() (AST, error), verbName string) ([]AST, error) {
	err := p.expectToken(TokenKindOpenBracket, "there should be a '(' here")
	if err != nil {
		return nil, err
	}

	// get a series of sub-clauses.
	p.lexer.GetToken()
	var asts []AST
	semiErrorMessage := fmt.Sprint("I really wanted a semicolon between these '", verbName, "'s")
	for {
		// is it a terminating ')'?
		closeBracketToken, err := p.lexer.PeekToken(0)
		if err != nil {
			return nil, err
		}
		if closeBracketToken.TokenKind() == TokenKindCloseBracket {
			break
		}

		// parse a sub-clause.
		newClause, err := parseClause()
		if err != nil {
			return nil, err
		}

		// get a semicolon separator.
		err = p.expectToken(TokenKindSemicolon, semiErrorMessage)
		if err != nil {
			return nil, err
		}

		asts = append(asts, newClause)
	}

	return asts, nil
}

// parseGroupMulti parses a group of some other clause, surrounded by brackets and
// with semicolons after each entry.
func (p *Parser) parseGroupMulti(parseClause func() ([]AST, error), verbName string) ([]AST, error) {
	err := p.expectToken(TokenKindOpenBracket, "there should be a '(' here")
	if err != nil {
		return nil, err
	}

	// get a series of sub-clauses.
	p.lexer.GetToken()
	var asts []AST
	semiErrorMessage := fmt.Sprint("I really wanted a semicolon between these '", verbName, "'s")
	for {
		// is it a terminating ')'?
		closeBracketToken, err := p.lexer.PeekToken(0)
		if err != nil {
			return nil, err
		}
		if closeBracketToken.TokenKind() == TokenKindCloseBracket {
			break
		}

		// parse a sub-clause.
		newClauses, err := parseClause()
		if err != nil {
			return nil, err
		}

		// get a semicolon separator.
		err = p.expectToken(TokenKindSemicolon, semiErrorMessage)
		if err != nil {
			return nil, err
		}

		asts = append(asts, newClauses...)
	}

	return asts, nil
}

// parseOptionallyQualifiedIdentifier parses an identifier with or without a package name.
// OptionallyQualifiedIdent = identifier | QualifiedIdent .
// QualifiedIdent = PackageName "." identifier .
func (p *Parser) parseOptionallyQualifiedIdentifier() (AST, error) {
	// check that it's an identifier of some sort
	tok, err := p.lexer.GetToken()
	if err != nil {
		return nil, err
	}
	if tok.TokenKind() != TokenKindIdentifier {
		return nil, NewError(p.filename, tok.Pos(), "if you could just put an identifier here that'd be greeeat")
	}

	ast := ASTIdentifier{tok.Pos(), "", tok.(StringToken).strVal}

	// might be followed by a '.'
	tok, err = p.lexer.PeekToken(0)
	if tok.TokenKind() == TokenKindDot {
		p.lexer.GetToken()

		// get a following identifier.
		if tok.TokenKind() != TokenKindIdentifier {
			return nil, NewError(p.filename, tok.Pos(), "if you could just put an identifier here that'd be greeeat")
		}

		ast.packageName = ast.name
		ast.name = tok.(StringToken).strVal
	}

	return ast, nil
}

// parseSignature parses a function/method signature.
// Signature      = Parameters [ Result ] .
// Result         = Parameters | Type .
func (p *Parser) parseSignature() ([]AST, []AST, error) {
	// get a bracket-enclosed parameter list
	params, err := p.parseBracketedParameterList()
	if err != nil {
		return nil, nil, err
	}

	// is there a return type?
	returnTok, err := p.lexer.PeekToken(0)
	if err != nil {
		return nil, nil, err
	}

	var returns []AST
	if returnTok.TokenKind() == TokenKindOpenBracket {
		// it's a bracketed return list.
		returns, err = p.parseBracketedParameterList()
		if err != nil {
			return nil, nil, err
		}
	} else {
		// is it a single data type?
		match, returnType, err := p.parseDataType()
		if err != nil {
			return nil, nil, err
		}
		if match {
			// yes, set this return type.
			returns = []AST{ASTParameterDecl{nil, returnType}}
		}
	}

	return params, returns, nil
}

// parseBracketedParameterList parses a parameter list surrounded by brackets.
// Parameters     = "(" [ ParameterList [ "," ] ] ")" .
// ParameterList  = ParameterDecl { "," ParameterDecl } .
// ParameterDecl  = [ IdentifierList ] [ "..." ] Type .
func (p *Parser) parseBracketedParameterList() ([]AST, error) {
	// get the open bracket
	err := p.expectToken(TokenKindOpenBracket, "parameter lists should start with '('")
	if err != nil {
		return nil, err
	}

	// get a series of parameter declarations.
	var params []AST
	for {
		// get a parameter declaration.
		newParams, err := p.parseParameterDecl()
		if err != nil {
			return nil, err
		}

		params = append(params, newParams...)
	}

	return params, nil
}

// parseBracketedParameterList parses a parameter list surrounded by brackets.
// ParameterDecl  = [ IdentifierList ] [ "..." ] Type .
func (p *Parser) parseParameterDecl() ([]AST, error) {
	// get a list of identifiers
	idents, err := p.parseIdentifierList("parameter")
	if err != nil {
		return nil, err
	}

	// see if there's a "...".
	tok, err := p.lexer.PeekToken(0)
	if err != nil {
		return nil, err
	}

	if tok.TokenKind() == TokenKindEllipsis {
		idents = append(idents, ASTEllipsis{tok.Pos()})
	}

	// the next thing should be a type declaration.
	typeToken, err := p.lexer.PeekToken(0)
	if err != nil {
		return nil, err
	}

	match, typ, err := p.parseDataType()
	if err != nil {
		return nil, err
	}
	if !match {
		return nil, NewError(p.filename, typeToken.Pos(), "there's a missing type in this parameter list")
	}

	// return all the parameters, expanded.
	params := make([]AST, len(idents))
	for i, ident := range idents {
		params[i] = ASTParameterDecl{ident, typ}
	}

	return params, nil
}

// expectToken parses a required token.
func (p *Parser) expectToken(tk TokenKind, message string) error {
	_, err := p.expectTokenPos(tk, message)
	return err
}

// expectTokenPos parses a required token. It returns the position of the
// token.
func (p *Parser) expectTokenPos(tk TokenKind, message string) (SrcSpan, error) {
	// get a token
	tok, err := p.lexer.GetToken()
	if err != nil {
		return tok.Pos(), err
	}
	if tok.TokenKind() != tk {
		return tok.Pos(), NewError(p.filename, tok.Pos(), message)
	}

	return tok.Pos(), nil
}
