package meat

import (
	"fmt"
	"go/scanner"
	"go/token"
	"strings"
)

// go.go holds Go-aware plan validators. Reading diffs intentionally need not
// compile, but they should retain enough Go shape that a reviewer never sees a
// body whose function/control owner vanished or delimiters that no longer
// describe the original hunk.

type goSourceLine struct {
	index int
	text  string
}

type goScannedToken struct {
	token  token.Token
	lineNo int // physical original-diff line index; -1 for rendered text
}

type goDelimiters struct {
	braces   int
	parens   int
	brackets int
}

func validateGoStructure(lines []sourceLine, layout diffLayout, state planState, replacements map[int][]plannedReplacement) error {
	var problems []error
	seenHunks := make(map[int]bool)
	for i, kind := range layout.kinds {
		if !isHunkSource(kind) || layout.language[i] != sourceLanguageGo || layout.hunkID[i] < 0 {
			continue
		}
		seenHunks[layout.hunkID[i]] = true
	}
	for hunkID := range seenHunks {
		for _, side := range []byte{'-', '+'} {
			raw := goHunkSide(lines, layout, hunkID, side, state, replacements, false)
			if len(raw) == 0 {
				continue
			}
			visible := goHunkSide(lines, layout, hunkID, side, state, replacements, true)
			rawTokens := scanGoSource(raw)
			visibleTokens := scanGoSource(visible)
			if goDelimiterDelta(rawTokens) != goDelimiterDelta(visibleTokens) {
				problems = append(problems, fmt.Errorf("remove/fold/replace: Go hunk %d %s-side no longer preserves delimiter structure", hunkID, goSideName(side)))
			}
			problems = append(problems, hiddenGoOwnersWithVisibleBodies(rawTokens, state)...)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return joinEditPlanErrors(problems)
}

func goHunkSide(lines []sourceLine, layout diffLayout, hunkID int, side byte, state planState, replacements map[int][]plannedReplacement, visible bool) []goSourceLine {
	var result []goSourceLine
	for i, line := range lines {
		if layout.hunkID[i] != hunkID || layout.language[i] != sourceLanguageGo || !isHunkSource(layout.kinds[i]) || len(line.text) == 0 {
			continue
		}
		marker := line.text[0]
		if marker != ' ' && marker != side {
			continue
		}
		if visible && state.hidden[i] {
			continue
		}
		body := strings.TrimSuffix(line.text[1:], "\n")
		if visible && len(replacements[i+1]) > 0 {
			body = applyPlannedReplacements(body, replacements[i+1])
		}
		result = append(result, goSourceLine{index: i, text: body})
	}
	return result
}

func scanGoSource(lines []goSourceLine) []goScannedToken {
	if len(lines) == 0 {
		return nil
	}
	lineIndexes := make([]int, 0, len(lines))
	var source strings.Builder
	for _, line := range lines {
		lineIndexes = append(lineIndexes, line.index)
		source.WriteString(line.text)
		source.WriteByte('\n')
	}
	file := token.NewFileSet().AddFile("reading-diff.go", -1, source.Len())
	var s scanner.Scanner
	s.Init(file, []byte(source.String()), nil, scanner.ScanComments)
	var result []goScannedToken
	for {
		pos, tok, _ := s.Scan()
		if tok == token.EOF {
			return result
		}
		line := file.Position(pos).Line
		lineNo := -1
		if line > 0 && line <= len(lineIndexes) {
			lineNo = lineIndexes[line-1]
		}
		result = append(result, goScannedToken{token: tok, lineNo: lineNo})
	}
}

func goDelimiterDelta(tokens []goScannedToken) goDelimiters {
	var result goDelimiters
	for _, scanned := range tokens {
		switch scanned.token {
		case token.LBRACE:
			result.braces++
		case token.RBRACE:
			result.braces--
		case token.LPAREN:
			result.parens++
		case token.RPAREN:
			result.parens--
		case token.LBRACK:
			result.brackets++
		case token.RBRACK:
			result.brackets--
		}
	}
	return result
}

func hiddenGoOwnersWithVisibleBodies(tokens []goScannedToken, state planState) []error {
	byLine := make(map[int][]token.Token)
	for _, scanned := range tokens {
		if scanned.lineNo >= 0 {
			byLine[scanned.lineNo] = append(byLine[scanned.lineNo], scanned.token)
		}
	}
	type openBrace struct {
		lineNo int
		owner  bool
	}
	var stack []openBrace
	var problems []error
	for _, scanned := range tokens {
		switch scanned.token {
		case token.LBRACE:
			stack = append(stack, openBrace{lineNo: scanned.lineNo, owner: goLineOwnsBlock(byLine[scanned.lineNo])})
		case token.RBRACE:
			if len(stack) == 0 {
				continue
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if open.owner && open.lineNo >= 0 && !state.represented(open.lineNo) && goVisibleBodyBetween(byLine, state, open.lineNo, scanned.lineNo) {
				problems = append(problems, fmt.Errorf("remove/fold: hides Go block owner on line %d while its body remains visible", open.lineNo+1))
			}
		}
	}
	return problems
}

func goLineOwnsBlock(tokens []token.Token) bool {
	var hasType, hasShape bool
	for _, tok := range tokens {
		switch tok {
		case token.FUNC, token.IF, token.FOR, token.SWITCH, token.SELECT, token.ELSE:
			return true
		case token.TYPE:
			hasType = true
		case token.STRUCT, token.INTERFACE:
			hasShape = true
		}
	}
	return hasType && hasShape
}

func goVisibleBodyBetween(tokens map[int][]token.Token, state planState, start, end int) bool {
	for line := start + 1; line < end; line++ {
		if !state.represented(line) {
			continue
		}
		for _, tok := range tokens[line] {
			switch tok {
			case token.COMMENT, token.SEMICOLON, token.LBRACE, token.RBRACE:
				continue
			default:
				return true
			}
		}
	}
	return false
}

func goSideName(side byte) string {
	if side == '+' {
		return "new"
	}
	return "old"
}

func goReplacementPreservesStructure(before, after string) bool {
	beforeTokens := goBlockTokens(scanGoSource([]goSourceLine{{index: -1, text: before}}))
	afterTokens := goBlockTokens(scanGoSource([]goSourceLine{{index: -1, text: after}}))
	if len(beforeTokens) != len(afterTokens) {
		return false
	}
	for i := range beforeTokens {
		if beforeTokens[i] != afterTokens[i] {
			return false
		}
	}
	return true
}

// goBlockTokens intentionally excludes parentheses and brackets. A local
// reading elision may legitimately collapse a one-line conversion or call,
// while removing a brace can detach a function/control owner or erase a
// composite-literal boundary.
func goBlockTokens(tokens []goScannedToken) []token.Token {
	var result []token.Token
	for _, scanned := range tokens {
		switch scanned.token {
		case token.LBRACE, token.RBRACE:
			result = append(result, scanned.token)
		}
	}
	return result
}
