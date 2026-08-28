// typescript.go holds TypeScript-aware plan validators. Reading diffs
// intentionally need not compile, but they should retain enough TypeScript
// shape that a reviewer never sees a body whose class/interface/type owner
// vanished or a multiline call, generic, template literal, or interface body
// whose opening delimiter was edited away.

package meat

import (
	"fmt"
	"strings"
)

type tsSourceLine struct {
	index int
	text  string
}

type tsScannedToken struct {
	kind   tsTokenKind
	lineNo int // physical original-diff line index; -1 for rendered text
}

type tsTokenKind uint8

const (
	tsTokOther tsTokenKind = iota
	tsTokLBrace
	tsTokRBrace
	tsTokLParen
	tsTokRParen
	tsTokLBracket
	tsTokRBracket
	tsTokLAngle
	tsTokRAngle
	tsTokSemicolon
	tsTokComma
)

// validateTypeScriptStructure keeps TypeScript plan semantics honest: an
// abridged hunk must not lose its delimiter structure, a hidden
// class/interface/type/enum/module/function/control owner must not leave a
// visible body stranded in the reading diff, and a hidden decorator must
// not leave its declaration visible. The checks are text-level — a
// TypeScript-aware diff is for reading, not for compiling.
//
// Per-fold balancedness: each fold must hide a region whose own
// delimiter counts sum to zero (or the fold's replacements must restore
// the balance). This lets a single fold hide a multiline JSX/generic
// expression that contributes multiple paired delimiters, while still
// rejecting a fold that opens or closes a structural token outside its
// range. The per-hunk delta check is kept identical to Go's: the
// visible-side net counts must equal the raw-side net counts, so any
// removal or replacement that breaks balance fires here.
func validateTypeScriptStructure(lines []sourceLine, layout diffLayout, state planState, replacements map[int][]plannedReplacement) error {
	var problems []error
	seenHunks := make(map[int]bool)
	for i, kind := range layout.kinds {
		if !isHunkSource(kind) || layout.language[i] != sourceLanguageTypeScript || layout.hunkID[i] < 0 {
			continue
		}
		seenHunks[layout.hunkID[i]] = true
	}
	for hunkID := range seenHunks {
		for _, side := range []byte{'-', '+'} {
			raw := tsHunkSide(lines, layout, hunkID, side, state, replacements, false)
			if len(raw) == 0 {
				continue
			}
			visible := tsHunkSide(lines, layout, hunkID, side, state, replacements, true)
			rawTokens, rawLineText := scanTypeScriptSource(raw)
			visibleTokens, _ := scanTypeScriptSource(visible)
			if tsDelimiterDelta(rawTokens) != tsDelimiterDelta(visibleTokens) {
				problems = append(problems, fmt.Errorf("remove/fold/replace: TypeScript hunk %d %s-side no longer preserves delimiter structure", hunkID, tsSideName(side)))
			}
			problems = append(problems, unbalancedTypeScriptFolds(lines, layout, hunkID, side, state)...)
			problems = append(problems, hiddenTypeScriptDetachedDecorators(rawLineText, state)...)
			problems = append(problems, hiddenTypeScriptOwnersWithVisibleBodies(rawTokens, rawLineText, state)...)
		}
	}
	if len(problems) == 0 {
		return nil
	}
	return joinEditPlanErrors(problems)
}

// unbalancedTypeScriptFolds returns one error per fold whose hidden
// region's own net delimiter delta is non-zero. A non-zero delta means
// the fold opens or closes a structural token whose pair is outside the
// fold, which the visible side will then lack.
func unbalancedTypeScriptFolds(lines []sourceLine, layout diffLayout, hunkID int, side byte, state planState) []error {
	var problems []error
	for fi := range state.folds {
		fold := state.folds[fi]
		if layout.hunkID[fold.StartLine-1] != hunkID {
			continue
		}
		if layout.language[fold.StartLine-1] != sourceLanguageTypeScript {
			continue
		}
		// Only inspect the side whose marker matches the fold marker;
		// a fold on the `-` side does not affect a `+` reader.
		if fold.marker != side {
			continue
		}
		var region []tsSourceLine
		for n := fold.StartLine; n <= fold.EndLine; n++ {
			lineNo := n - 1
			if !isHunkSource(layout.kinds[lineNo]) {
				continue
			}
			region = append(region, tsSourceLine{index: -1, text: strings.TrimSuffix(lines[lineNo].text[1:], "\n")})
		}
		tokens, _ := scanTypeScriptSource(region)
		delta := tsDelimiterDelta(tokens)
		if delta.braces != 0 || delta.parens != 0 || delta.brackets != 0 || delta.angles != 0 {
			problems = append(problems, fmt.Errorf("fold[%d]: TypeScript fold on lines %d-%d hides an unbalanced %s region; fold only complete balanced groups", fi, fold.StartLine, fold.EndLine, tsDeltaDescribe(delta)))
		}
	}
	return problems
}

func tsDeltaDescribe(d tsDelimiters) string {
	var parts []string
	if d.braces != 0 {
		parts = append(parts, fmt.Sprintf("{%+d}", d.braces))
	}
	if d.parens != 0 {
		parts = append(parts, fmt.Sprintf("(%+d)", d.parens))
	}
	if d.brackets != 0 {
		parts = append(parts, fmt.Sprintf("[%+d]", d.brackets))
	}
	if d.angles != 0 {
		parts = append(parts, fmt.Sprintf("<%+d>", d.angles))
	}
	if len(parts) == 0 {
		return "delimiter"
	}
	return strings.Join(parts, ",")
}

func tsHunkSide(lines []sourceLine, layout diffLayout, hunkID int, side byte, state planState, replacements map[int][]plannedReplacement, visible bool) []tsSourceLine {
	var result []tsSourceLine
	for i, line := range lines {
		if layout.hunkID[i] != hunkID || layout.language[i] != sourceLanguageTypeScript || !isHunkSource(layout.kinds[i]) || len(line.text) == 0 {
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
		result = append(result, tsSourceLine{index: i, text: body})
	}
	return result
}

// scanTypeScriptSource is a hand-rolled TypeScript tokenizer tuned for the
// kinds of structure meat cares about: braces, parens, brackets, and angle
// brackets in balance, with string/template/regex/comments skipped. The
// second return value maps each original physical line number to its
// rendered text on the scanned side, so the owner detector can recognize
// keyword prefixes (class, interface, type, etc.) without re-tokenizing.
func scanTypeScriptSource(lines []tsSourceLine) ([]tsScannedToken, map[int]string) {
	if len(lines) == 0 {
		return nil, nil
	}
	lineIndexes := make([]int, 0, len(lines))
	lineText := make(map[int]string, len(lines))
	var source strings.Builder
	for _, line := range lines {
		lineIndexes = append(lineIndexes, line.index)
		lineText[line.index] = line.text
		source.WriteString(line.text)
		source.WriteByte('\n')
	}
	text := source.String()
	var result []tsScannedToken
	type frame struct {
		kind  tsLexState
		delim byte
		// depth tracks nested braces/expressions inside a template-literal
		// ${...} interpolation so a brace inside the template does not
		// terminate the outer state.
		depth int
	}
	state := tsLexCode
	var stack []frame
	// angleBalance tracks how many unmatched `<` characters are open
	// in the current scope. A `>` is treated as an RAngle only when
	// angleBalance > 0 (an unclosed tag or generic); otherwise it is
	// a comparison operator and is ignored. This prevents
	// `> 0`, `> x`, `>= y`, and `=>` (handled separately) from being
	// miscounted as tag closers.
	angleBalance := 0
	jsxDepth := 0
	for i := 0; i < len(text); {
		line := lineAt(text, i)
		lineNo := -1
		if line > 0 && line <= len(lineIndexes) {
			lineNo = lineIndexes[line-1]
		}
		ch := text[i]
		if state == tsLexCode {
			if ch == '/' && i+1 < len(text) && text[i+1] == '/' {
				state = tsLexLineComment
				i += 2
				continue
			}
			if ch == '/' && i+1 < len(text) && text[i+1] == '*' {
				state = tsLexBlockComment
				i += 2
				continue
			}
			if ch == '"' || ch == '\'' {
				state = tsLexSingleString
				stack = append(stack, frame{kind: state, delim: ch})
				i++
				continue
			}
			if ch == '`' {
				state = tsLexTemplate
				stack = append(stack, frame{kind: state, delim: '`', depth: 0})
				i++
				continue
			}
			if ch == '/' && tsRegexAllowedAt(text, i) {
				state = tsLexRegex
				stack = append(stack, frame{kind: state, delim: '/'})
				i++
				continue
			}
			switch ch {
			case '{':
				if jsxDepth == 0 {
					result = append(result, tsScannedToken{kind: tsTokLBrace, lineNo: lineNo})
				}
			case '}':
				if jsxDepth == 0 {
					result = append(result, tsScannedToken{kind: tsTokRBrace, lineNo: lineNo})
				}
				if len(stack) > 0 && stack[len(stack)-1].kind == tsLexTemplate && stack[len(stack)-1].depth == 0 {
					stack = stack[:len(stack)-1]
				}
			case '(':
				result = append(result, tsScannedToken{kind: tsTokLParen, lineNo: lineNo})
			case ')':
				result = append(result, tsScannedToken{kind: tsTokRParen, lineNo: lineNo})
			case '[':
				result = append(result, tsScannedToken{kind: tsTokLBracket, lineNo: lineNo})
			case ']':
				result = append(result, tsScannedToken{kind: tsTokRBracket, lineNo: lineNo})
			case '<':
				if tsAngleOpenAt(text, i) {
					result = append(result, tsScannedToken{kind: tsTokLAngle, lineNo: lineNo})
					angleBalance++
					if jsx, closing, selfClosing := tsJSXTagAt(text, i); jsx {
						if closing {
							if jsxDepth > 0 {
								jsxDepth--
							}
						} else if !selfClosing {
							jsxDepth++
						}
					}
				}
			case '>':
				// `=>` is the arrow-function operator; the `>` is not
				// a tag closer. When we are inside an open paren
				// (which is where arrow-function parameters are
				// declared) the previous non-whitespace char is `=`,
				// so we look back through spaces and tabs.
				if i > 0 {
					j := i - 1
					for j >= 0 && (text[j] == ' ' || text[j] == '\t') {
						j--
					}
					if j >= 0 && text[j] == '=' {
						// Arrow: skip the `>` and continue; the
						// body may be a single expression or a
						// block, neither of which is tag syntax.
						i++
						continue
					}
				}
				// `>=` is a comparison operator; the `>` is not a
				// tag closer.
				if i+1 < len(text) && text[i+1] == '=' {
					i += 2
					continue
				}
				// Only count the `>` as an RAngle when there is an
				// unmatched `<` to close. Otherwise it is a
				// comparison operator (`a > b`, `x > 0`) and is
				// ignored. This keeps the structural delta
				// meaningful in real TypeScript code where `>` is
				// overwhelmingly a comparison.
				if angleBalance > 0 {
					angleBalance--
					result = append(result, tsScannedToken{kind: tsTokRAngle, lineNo: lineNo})
				}
			case ';':
				result = append(result, tsScannedToken{kind: tsTokSemicolon, lineNo: lineNo})
			case ',':
				result = append(result, tsScannedToken{kind: tsTokComma, lineNo: lineNo})
			}
			i++
			continue
		}
		if state == tsLexLineComment {
			if ch == '\n' {
				state = tsLexCode
			}
			i++
			continue
		}
		if state == tsLexBlockComment {
			if ch == '*' && i+1 < len(text) && text[i+1] == '/' {
				state = tsLexCode
				i += 2
				continue
			}
			i++
			continue
		}
		if state == tsLexSingleString {
			top := stack[len(stack)-1]
			if ch == '\\' {
				i += 2
				continue
			}
			if ch == top.delim {
				stack = stack[:len(stack)-1]
				state = tsLexCode
				i++
				continue
			}
			if ch == '\n' {
				// Unterminated single-line string: drop back to code so a
				// malformed literal does not swallow the rest of the hunk.
				stack = stack[:len(stack)-1]
				state = tsLexCode
				continue
			}
			i++
			continue
		}
		if state == tsLexTemplate {
			top := stack[len(stack)-1]
			if ch == '\\' {
				i += 2
				continue
			}
			if top.depth > 0 {
				if ch == '{' {
					top.depth++
				} else if ch == '}' {
					top.depth--
					if top.depth == 0 {
						state = tsLexTemplate
					}
				}
				stack[len(stack)-1] = top
				if ch == '{' || ch == '}' {
					i++
					continue
				}
			}
			if ch == '`' {
				stack = stack[:len(stack)-1]
				state = tsLexCode
				i++
				continue
			}
			if ch == '$' && i+1 < len(text) && text[i+1] == '{' {
				top.depth++
				stack[len(stack)-1] = top
				i += 2
				continue
			}
			i++
			continue
		}
		if state == tsLexRegex {
			top := stack[len(stack)-1]
			if ch == '\\' {
				i += 2
				continue
			}
			if ch == '[' {
				top.delim = '['
				stack[len(stack)-1] = top
				i++
				continue
			}
			if top.delim == '[' {
				if ch == ']' {
					top.delim = '/'
					stack[len(stack)-1] = top
				}
				i++
				continue
			}
			if ch == top.delim {
				// Consume regex flags.
				stack = stack[:len(stack)-1]
				state = tsLexCode
				i++
				for i < len(text) && isTypeScriptIdentStart(text[i]) {
					i++
				}
				continue
			}
			if ch == '\n' {
				// Unterminated regex: drop back to code.
				stack = stack[:len(stack)-1]
				state = tsLexCode
				continue
			}
			i++
			continue
		}
		i++
	}
	return result, lineText
}

type tsLexState uint8

const (
	tsLexCode tsLexState = iota
	tsLexLineComment
	tsLexBlockComment
	tsLexSingleString
	tsLexTemplate
	tsLexRegex
)

// tsDelimiterDelta sums net changes in the delimiter kinds we care about.
// Angle brackets are tracked so a folded or replaced generic signature
// cannot silently vanish; other tokens are excluded so a local elision
// that legitimately drops a semicolon or comma is not flagged.
func tsDelimiterDelta(tokens []tsScannedToken) tsDelimiters {
	var result tsDelimiters
	for _, scanned := range tokens {
		switch scanned.kind {
		case tsTokLBrace:
			result.braces++
		case tsTokRBrace:
			result.braces--
		case tsTokLParen:
			result.parens++
		case tsTokRParen:
			result.parens--
		case tsTokLBracket:
			result.brackets++
		case tsTokRBracket:
			result.brackets--
		case tsTokLAngle:
			result.angles++
		case tsTokRAngle:
			result.angles--
		}
	}
	return result
}

type tsDelimiters struct {
	braces   int
	parens   int
	brackets int
	angles   int
}

func hiddenTypeScriptOwnersWithVisibleBodies(tokens []tsScannedToken, lineText map[int]string, state planState) []error {
	byLine := make(map[int][]tsTokenKind)
	for _, scanned := range tokens {
		if scanned.lineNo >= 0 {
			byLine[scanned.lineNo] = append(byLine[scanned.lineNo], scanned.kind)
		}
	}
	type openBrace struct {
		lineNo int
		owner  bool
		kind   string
	}
	var stack []openBrace
	var problems []error
	for _, scanned := range tokens {
		if scanned.kind != tsTokLBrace && scanned.kind != tsTokRBrace {
			continue
		}
		if scanned.kind == tsTokLBrace {
			owner, kind := tsLineOwnsBlock(lineText[scanned.lineNo], lineText, scanned.lineNo)
			stack = append(stack, openBrace{lineNo: scanned.lineNo, owner: owner, kind: kind})
			continue
		}
		if len(stack) == 0 {
			continue
		}
		open := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if open.owner && open.lineNo >= 0 && !state.represented(open.lineNo) && tsVisibleBodyBetween(byLine, state, lineText, open.lineNo, scanned.lineNo) {
			problems = append(problems, fmt.Errorf("remove/fold: hides TypeScript %s owner on line %d while its body remains visible", open.kind, open.lineNo+1))
		}
	}
	return problems
}

// tsLineOwnsBlock reports whether the line that opens a brace block is a
// TypeScript owner whose body should not be detached. The owner-keyword
// list intentionally covers every brace-introducing declaration/control
// construct in TypeScript so a hidden `class`/`interface`/`enum`/`type`
// alias body or `function`/`if`/`for`/`switch` body is detected.
func tsLineOwnsBlock(text string, lineText map[int]string, lineNo int) (bool, string) {
	if text == "" {
		return false, ""
	}
	trimmed := trimTypeScriptCode(text)
	if trimmed == "" {
		return false, ""
	}
	for _, keyword := range tsOwnerKeywords {
		if strings.HasPrefix(trimmed, keyword) {
			// A bare `type Foo = ...` line does not open a brace, so it
			// is not a structural owner; but a `type Foo = { ... }` does.
			// The brace stack at this line will tell us — the caller only
			// calls this when an LBRACE is present on the line.
			return true, strings.TrimSpace(keyword)
		}
	}
	// A `@decorator` line followed by a brace on the next line is still
	// an owner; the decorator must stay with the declaration.
	if strings.HasPrefix(trimmed, "@") {
		return true, "decorator"
	}
	// Check the immediately preceding physical line for a decorator.
	if lineNo > 0 {
		if prev, ok := lineText[lineNo-1]; ok {
			if strings.HasPrefix(strings.TrimSpace(trimTypeScriptCode(prev)), "@") {
				return true, "decorator"
			}
		}
	}
	return false, ""
}

var tsOwnerKeywords = []string{
	"class ",
	"abstract class ",
	"interface ",
	"enum ",
	"module ",
	"namespace ",
	"function ",
	"async function ",
	"function* ",
	"if ",
	"for ",
	"while ",
	"switch ",
	"try ",
	"catch ",
	"finally ",
	"do ",
	"static ",
	"public ",
	"private ",
	"protected ",
	"readonly ",
	"abstract ",
	"override ",
	"export ",
	"declare ",
	"type ",
}

func tsVisibleBodyBetween(tokens map[int][]tsTokenKind, state planState, lineText map[int]string, start, end int) bool {
	for line := start + 1; line < end; line++ {
		if !state.represented(line) {
			continue
		}
		// A body row with any non-delimiter token, or any non-whitespace
		// text at all (the typeScript tokenizer emits only structural
		// tokens, so identifier- and string-bearing lines register as
		// empty in `tokens`).
		for _, tok := range tokens[line] {
			switch tok {
			case tsTokLBrace, tsTokRBrace, tsTokSemicolon, tsTokComma:
				continue
			default:
				return true
			}
		}
		if text := strings.TrimSpace(lineText[line]); text != "" {
			return true
		}
	}
	return false
}

func tsSideName(side byte) string {
	if side == '+' {
		return "new"
	}
	return "old"
}

// hiddenTypeScriptDetachedDecorators reports hidden decorator lines whose
// following declaration line is still represented. A `@decorator` is
// structurally meaningless without its declaration; the plan must either
// keep both, fold both, or remove both. This check runs per hunk-side
// using the raw text so folded or replacement-edited bodies are inspected
// in their original form.
func hiddenTypeScriptDetachedDecorators(rawLineText map[int]string, state planState) []error {
	var problems []error
	if len(rawLineText) == 0 {
		return nil
	}
	// Collect the set of physical line indices belonging to this side,
	// in ascending order. tsHunkSide already filters to the side so
	// every entry in rawLineText is on this side.
	var sideLines []int
	for lineNo := range rawLineText {
		sideLines = append(sideLines, lineNo)
	}
	for i := 1; i < len(sideLines); i++ {
		for j := i; j > 0 && sideLines[j-1] > sideLines[j]; j-- {
			sideLines[j-1], sideLines[j] = sideLines[j], sideLines[j-1]
		}
	}
	for i, lineNo := range sideLines {
		if !state.hidden[lineNo] {
			continue
		}
		body, ok := rawLineText[lineNo]
		if !ok {
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(trimTypeScriptCode(body)), "@") {
			continue
		}
		// Find the next non-blank line in the same hunk.
		for j := i + 1; j < len(sideLines); j++ {
			next := sideLines[j]
			nextBody, ok := rawLineText[next]
			if !ok {
				continue
			}
			if strings.TrimSpace(trimTypeScriptCode(nextBody)) == "" {
				continue
			}
			if state.represented(next) {
				problems = append(problems, fmt.Errorf("remove/fold: hides TypeScript decorator on line %d while its declaration on line %d remains visible", lineNo+1, next+1))
			}
			break
		}
	}
	return problems
}

// tsReplacementPreservesStructure verifies a single-line replacement keeps
// the per-line brace and angle-bracket balance identical. Parens, brackets,
// semicolons, and commas are intentionally excluded so a legitimate local
// elision (e.g. dropping a few arguments or a trailing semicolon) is not
// rejected; braces and generics are the structural anchors of TypeScript.
func tsReplacementPreservesStructure(before, after string) bool {
	beforeTokens, _ := scanTypeScriptSource([]tsSourceLine{{index: -1, text: before}})
	afterTokens, _ := scanTypeScriptSource([]tsSourceLine{{index: -1, text: after}})
	beforeBlock := tsBlockTokens(beforeTokens)
	afterBlock := tsBlockTokens(afterTokens)
	if len(beforeBlock) != len(afterBlock) {
		return false
	}
	for i := range beforeBlock {
		if beforeBlock[i] != afterBlock[i] {
			return false
		}
	}
	return true
}

func tsReplacementPreservesOwner(before, after string) bool {
	beforeOwner, _ := tsLineOwnsBlock(before, nil, -1)
	afterOwner, _ := tsLineOwnsBlock(after, nil, -1)
	return !beforeOwner || afterOwner
}

func tsAngleOpenAt(text string, at int) bool {
	if at+1 >= len(text) {
		return false
	}
	next := text[at+1]
	if next == '=' || next == '<' {
		return false
	}
	if next == '/' {
		return at+2 < len(text) && isTypeScriptIdentStart(text[at+2])
	}
	if next == '.' && at+3 < len(text) && text[at+2] == '.' && text[at+3] == '.' {
		return true
	}
	if !isTypeScriptIdentStart(next) {
		return false
	}
	// `a < b` is a comparison; a type argument such as `Map<T>` has no
	// whitespace before the opener. JSX and generic declarations commonly
	// have whitespace before `<`, so the following identifier is the useful
	// discriminator for this lightweight scanner.
	if at > 0 && (text[at-1] == ' ' || text[at-1] == '\t') {
		return next >= 'A' && next <= 'Z'
	}
	return true
}

func tsJSXTagAt(text string, at int) (jsx, closing, selfClosing bool) {
	if at+1 >= len(text) {
		return false, false, false
	}
	start := at + 1
	if text[start] == '/' {
		closing = true
		start++
	}
	if start >= len(text) || !isTypeScriptIdentStart(text[start]) {
		return false, false, false
	}
	if at > 0 && isTypeScriptIdentContinue(text[at-1]) {
		return false, false, false
	}
	end := strings.IndexByte(text[start:], '>')
	if end < 0 {
		return false, false, false
	}
	tagBody := strings.TrimSpace(text[start : start+end])
	selfClosing = !closing && strings.HasSuffix(tagBody, "/")
	if !closing && !selfClosing {
		nameEnd := start
		for nameEnd < len(text) && isTypeScriptIdentContinue(text[nameEnd]) {
			nameEnd++
		}
		if !strings.Contains(text[start+end+1:], "</"+text[start:nameEnd]) {
			return false, false, false
		}
	}
	return true, closing, selfClosing
}

func tsBlockTokens(tokens []tsScannedToken) []tsTokenKind {
	var result []tsTokenKind
	for _, scanned := range tokens {
		switch scanned.kind {
		case tsTokLBrace, tsTokRBrace, tsTokLAngle, tsTokRAngle:
			result = append(result, scanned.kind)
		}
	}
	return result
}

// trimTypeScriptCode strips leading whitespace and a trailing line comment
// from a line of TypeScript source. It does not understand strings or
// template literals — those are handled by the line-level text in the
// owner check, where the line is already the hunk source line and not a
// multi-line string interior.
func trimTypeScriptCode(text string) string {
	trimmed := strings.TrimSpace(text)
	if i := strings.Index(trimmed, "//"); i >= 0 {
		// Only strip if the // is outside any string on this single line.
		if !lineHasUnclosedString(trimmed[:i]) {
			trimmed = strings.TrimSpace(trimmed[:i])
		}
	}
	return trimmed
}

func lineHasUnclosedString(s string) bool {
	var quote byte
	escaped := false
	for i := 0; i < len(s); i++ {
		b := s[i]
		if escaped {
			escaped = false
			continue
		}
		if b == '\\' {
			escaped = true
			continue
		}
		if quote != 0 {
			if b == quote {
				quote = 0
			}
			continue
		}
		if b == '"' || b == '\'' || b == '`' {
			quote = b
		}
	}
	return quote != 0
}

// tsRegexAllowedAt reports whether a `/` at offset i is the start of a
// regex literal. The predicate is a conservative approximation of the
// ECMAScript grammar: a `/` is a regex when it follows a position where
// an expression is expected, and a division otherwise. `=` is treated
// as value-producing, so `n /= 2` is read as division, while `n = /re/`
// is a regex.
func tsRegexAllowedAt(text string, i int) bool {
	if i == 0 {
		return true
	}
	j := i - 1
	for j >= 0 && (text[j] == ' ' || text[j] == '\t') {
		j--
	}
	if j < 0 {
		return true
	}
	prev := text[j]
	switch prev {
	case ',', ';', ':', '(', '[', '!', '?', '{', '+', '-', '*', '%', '^', '~', '\n':
		return true
	case '=':
		// `=` is an expression-expected context (assignment). It is a
		// regex literal, UNLESS this `/` is the second half of a
		// compound assignment operator (`/=`), in which case the char
		// before `=` is itself `/` or `*` or `+` etc. — we already
		// classified those as value-producing in their own right.
		// Detect `/=` directly: if the char at j-1 (before skipping
		// spaces) is `=`, check the char before the `=`. If that is
		// `/` or `*` or `+` or `-` or `%`, treat as compound division.
		if j-1 >= 0 {
			p2 := text[j-1]
			switch p2 {
			case '/', '*', '+', '-', '%', '<', '>', '!', '=', '&', '|', '^', '?', ':':
				return false
			}
		}
		return true
	case '/':
		// `//` and `/=` are operators, not regex starts. `/re/` after
		// another `/` is division (`a / /re/`); the second `/` has
		// division semantics. The line-comment case is handled at the
		// caller before this predicate is consulted.
		return false
	case ')':
		// After a `)`, the preceding expression is closed; a `/` is more
		// often a division than a regex.
		return false
	case '}', ']', '>', '<':
		// After a closing delimiter or an opening angle bracket (e.g.
		// the start of a closing JSX tag like `</button>`), the
		// previous expression is closed; `/` is division.
		return false
	}
	if isTypeScriptIdentContinue(prev) {
		// An identifier or number is a value, so `/` is division —
		// unless that identifier is a statement keyword (return, in,
		// typeof, void, throw, ...), in which case a `/` immediately
		// after it is a regex literal.
		return tsKeywordAllowsRegexBefore(text, j)
	}
	return true
}

// tsKeywordAllowsRegexBefore reports whether the identifier that ends at
// offset j (inclusive) in text is a statement-position keyword after which
// a `/` starts a regex literal.
func tsKeywordAllowsRegexBefore(text string, j int) bool {
	// Scan back over identifier characters to find the start of the
	// preceding word.
	end := j
	for j >= 0 && isTypeScriptIdentContinue(text[j]) {
		j--
	}
	word := text[j+1 : end+1]
	switch word {
	case "return", "in", "of", "typeof", "void", "throw", "delete", "new", "case", "do", "else", "yield", "await", "as", "is", "satisfies", "from", "instanceof":
		return true
	}
	return false
}

func isTypeScriptIdentStart(b byte) bool {
	return b == '_' || b == '$' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isTypeScriptIdentContinue(b byte) bool {
	return isTypeScriptIdentStart(b) || (b >= '0' && b <= '9')
}

func lineAt(text string, offset int) int {
	if offset > len(text) {
		offset = len(text)
	}
	line := 1
	for i := 0; i < offset; i++ {
		if text[i] == '\n' {
			line++
		}
	}
	return line
}
