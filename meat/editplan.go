package meat

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	maxReplacementBytes = 4 << 10
	maxSummaryBytes     = 500
)

// lineRange identifies an inclusive, 1-based range of physical lines in the
// original unified diff.
type lineRange struct {
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}

// lineReplacement replaces one exact span of one original hunk line. Old is
// matched against the source text after the diff's leading +, -, or context
// marker. New must be an elision projection of Old: characters may only be
// removed, with ... or … inserted as a reading placeholder.
type lineReplacement struct {
	Line int    `json:"line"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

// lineFold replaces a contiguous range of original hunk source lines with one
// machine-generated, correctly indented ellipsis row.
type lineFold struct {
	StartLine int `json:"start_line"`
	EndLine   int `json:"end_line"`
}

// editPlan contains only source-anchored operations. It has no model-authored
// prose and can be previewed before a summary is available.
type editPlan struct {
	Remove  []lineRange       `json:"remove"`
	Replace []lineReplacement `json:"replace"`
	Fold    []lineFold        `json:"fold"`
}

// submission is a complete edit plan plus its one-line summary.
type submission struct {
	Remove  []lineRange       `json:"remove"`
	Replace []lineReplacement `json:"replace"`
	Fold    []lineFold        `json:"fold"`
	Summary string            `json:"summary"`
}

func (s submission) plan() editPlan {
	return editPlan{Remove: s.Remove, Replace: s.Replace, Fold: s.Fold}
}

type sourceLine struct {
	text string
	eol  string
}

type plannedReplacement struct {
	lineReplacement
	planIndex int
	start     int
	end       int
}

type plannedFold struct {
	lineFold
	planIndex int
	marker    byte
	indent    string
	eol       string
}

type planState struct {
	hidden []bool
	folded []int // fold index for every folded source line; -1 otherwise
	foldAt []int // fold index emitted at its first source line; -1 otherwise
	folds  []plannedFold
}

func newPlanState(lines int) planState {
	s := planState{
		hidden: make([]bool, lines),
		folded: make([]int, lines),
		foldAt: make([]int, lines),
	}
	for i := 0; i < lines; i++ {
		s.folded[i] = -1
		s.foldAt[i] = -1
	}
	return s
}

func (s planState) represented(line int) bool {
	return !s.hidden[line] || s.folded[line] >= 0
}

type planStats struct {
	rawChanged     int
	visibleChanged int
	removedChanged int
	foldedChanged  int
	foldCount      int
	rawFiles       int
	visibleFiles   int
}

type compiledPlan struct {
	smartDiff string
	stats     planStats
	moves     []detectedMove
}

type diffLineKind uint8

const (
	diffLineOther diffLineKind = iota
	diffLineHeader
	diffLineIndex
	diffLineRenameFrom
	diffLineRenameTo
	diffLineCopyFrom
	diffLineCopyTo
	diffLineMailSignature
	diffLineOldFile
	diffLineNewFile
	diffLineHunkHeader
	diffLineHunkContext
	diffLineHunkChange
	diffLineNoNewline
)

type sourceLanguage uint8

const (
	sourceLanguageUnknown sourceLanguage = iota
	sourceLanguageGo
	sourceLanguagePython
	sourceLanguageJavaScript
	sourceLanguageRust
	sourceLanguageC
	sourceLanguageJava
)

type diffLayout struct {
	kinds       []diffLineKind
	markerOwner []int
	python      []bool
	language    []sourceLanguage
	fileID      []int
	hunkID      []int
	problems    []error
}

// splitSourceLines splits text into physical lines while retaining each line's
// exact ending. A final newline belongs to the preceding line and does not
// create a phantom empty line.
func splitSourceLines(text string) []sourceLine {
	if text == "" {
		return nil
	}
	var lines []sourceLine
	for len(text) > 0 {
		i := strings.IndexByte(text, '\n')
		if i < 0 {
			lines = append(lines, sourceLine{text: text})
			break
		}
		line, eol := text[:i], "\n"
		if strings.HasSuffix(line, "\r") {
			line = strings.TrimSuffix(line, "\r")
			eol = "\r\n"
		}
		lines = append(lines, sourceLine{text: line, eol: eol})
		text = text[i+1:]
	}
	return lines
}

func validateSupportedDiff(diff string) error {
	lines := splitSourceLines(diff)
	if err := validateSupportedDiffLines(lines); err != nil {
		return err
	}
	layout := analyzeDiff(lines)
	if len(layout.problems) > 0 {
		return joinEditPlanErrors(layout.problems)
	}
	return nil
}

func isGitDiffHeader(line string) bool {
	return strings.HasPrefix(line, "diff --git ")
}

func validateSupportedDiffLines(lines []sourceLine) error {
	inPatch := false
	for i, line := range lines {
		switch {
		case isFormatPatchSignature(line.text):
			inPatch = false
		case strings.HasPrefix(line.text, "diff --cc "), strings.HasPrefix(line.text, "diff --combined "):
			return fmt.Errorf("combined diff on line %d is unsupported; use a normal first-parent or two-tree diff", i+1)
		case isGitDiffHeader(line.text):
			inPatch = true
		case isRawOldFileHeader(lines, i):
			inPatch = true
		}
		if inPatch && strings.HasPrefix(line.text, "@@@") {
			return fmt.Errorf("combined diff hunk on line %d is unsupported; use a normal first-parent or two-tree diff", i+1)
		}
	}
	return nil
}

// numberedDiff adds a display-only 1-based line-number gutter for the model.
// The gutter is not part of the source and is never copied into the result.
func numberedDiff(diff string) string {
	lines := splitSourceLines(diff)
	if len(lines) == 0 {
		return ""
	}
	width := len(strconv.Itoa(len(lines)))
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%*d|%s\n", width, i+1, line.text)
	}
	return b.String()
}

// applyEditPlan validates the summary and atomically applies its source-anchored
// plan to raw.
func applyEditPlan(raw string, in submission) (string, error) {
	compiled, err := compileSubmission(raw, in)
	if err != nil {
		return "", err
	}
	return compiled.smartDiff, nil
}

func compileSubmission(raw string, in submission) (compiledPlan, error) {
	var problems []error
	if err := validateSummary(in.Summary); err != nil {
		problems = append(problems, err)
	}
	compiled, err := compileEditPlan(raw, in.plan())
	if err != nil {
		problems = append(problems, err)
	}
	if len(problems) > 0 {
		return compiledPlan{}, joinEditPlanErrors(problems)
	}
	return compiled, nil
}

// compileEditPlan validates and applies a plan without requiring a summary, so
// the exact reading diff can be previewed before final submission.
func compileEditPlan(raw string, in editPlan) (compiledPlan, error) {
	var problems []error

	lines := splitSourceLines(raw)
	if err := validateSupportedDiffLines(lines); err != nil {
		return compiledPlan{}, err
	}
	layout := analyzeDiff(lines)
	if len(layout.problems) > 0 {
		return compiledPlan{}, joinEditPlanErrors(layout.problems)
	}
	moves := detectExactMoves(lines, layout)
	mandatoryRemovals := mandatoryImportRemovalPlan(lines, layout)
	mandatoryHidden := mandatoryRemovalMask(len(lines), mandatoryRemovals)
	applyMandatoryMovePrecedence(moves, mandatoryHidden)
	state := newPlanState(len(lines))
	copy(state.hidden, mandatoryHidden)
	modelRemoved := make([]bool, len(lines))
	removalsValid := true
	for i, r := range in.Remove {
		if r.StartLine < 1 || r.EndLine < 1 || r.StartLine > r.EndLine {
			problems = append(problems, fmt.Errorf("remove[%d]: invalid inclusive range %d-%d", i, r.StartLine, r.EndLine))
			removalsValid = false
			continue
		}
		if r.EndLine > len(lines) {
			problems = append(problems, fmt.Errorf("remove[%d]: line %d is past end of diff (%d lines)", i, r.EndLine, len(lines)))
			removalsValid = false
			continue
		}
		firstOverlap := 0
		for n := r.StartLine; n <= r.EndLine; n++ {
			if modelRemoved[n-1] {
				if firstOverlap == 0 {
					firstOverlap = n
				}
				removalsValid = false
				continue
			}
			modelRemoved[n-1] = true
			state.hidden[n-1] = true
		}
		if firstOverlap != 0 {
			problems = append(problems, fmt.Errorf("remove[%d]: overlaps an earlier range at line %d", i, firstOverlap))
		}
	}
	foldsValid := true
	for i, f := range in.Fold {
		fold, err := prepareFold(lines, layout, f, i)
		if err != nil {
			problems = append(problems, err)
			foldsValid = false
			continue
		}
		mandatoryLines := 0
		for n := f.StartLine; n <= f.EndLine; n++ {
			if mandatoryHidden[n-1] {
				mandatoryLines++
			}
		}
		if mandatoryLines > 0 {
			if mandatoryLines != f.EndLine-f.StartLine+1 {
				problems = append(problems, fmt.Errorf("fold[%d]: crosses mandatory import removal and non-import source rows in range %d-%d; fold only the behavioral rows", i, f.StartLine, f.EndLine))
				foldsValid = false
			}
			// An import-only fold is redundant: mandatory removal wins and no
			// ellipsis placeholder is emitted for import scaffolding.
			continue
		}
		conflictLine := 0
		for n := f.StartLine; n <= f.EndLine; n++ {
			if state.hidden[n-1] {
				conflictLine = n
				break
			}
		}
		if conflictLine != 0 {
			kind := "remove"
			if state.folded[conflictLine-1] >= 0 {
				kind = "fold"
			}
			problems = append(problems, fmt.Errorf("fold[%d]: overlaps %s at line %d", i, kind, conflictLine))
			foldsValid = false
			continue
		}
		foldIndex := len(state.folds)
		state.folds = append(state.folds, fold)
		state.foldAt[f.StartLine-1] = foldIndex
		for n := f.StartLine; n <= f.EndLine; n++ {
			state.hidden[n-1] = true
			state.folded[n-1] = foldIndex
		}
	}
	if removalsValid && foldsValid {
		addMandatoryPythonSuitePlaceholders(lines, layout, &state, mandatoryHidden)
		if err := validateMoveSymmetry(moves, state, mandatoryHidden); err != nil {
			problems = append(problems, err)
		}
	}
	pythonValidationState := state
	if removalsValid && foldsValid {
		pythonValidationState = stateWithMandatoryImportsRepresented(state, mandatoryHidden, layout)
		if err := validateHiddenPythonOwners(lines, layout, pythonValidationState); err != nil {
			problems = append(problems, err)
		}
		if err := validateHiddenPythonBoundaries(lines, layout, pythonValidationState); err != nil {
			problems = append(problems, err)
		}
		if err := validateHiddenReferences(lines, layout, pythonValidationState); err != nil {
			problems = append(problems, err)
		}
		if err := validatePythonSuiteSkeleton(lines, layout, pythonValidationState); err != nil {
			problems = append(problems, err)
		}
	}

	replacements := make(map[int][]plannedReplacement)
	replacementsValid := true
	for i, r := range in.Replace {
		if r.Line < 1 || r.Line > len(lines) {
			problems = append(problems, fmt.Errorf("replace[%d]: line %d is outside the diff (1-%d)", i, r.Line, len(lines)))
			continue
		}
		if mandatoryHidden[r.Line-1] {
			// A replacement on compiler-hidden import scaffolding is redundant.
			// Ignore it so older cached/model plans that explicitly abridged an
			// import still merge cleanly with the mandatory plan.
			continue
		}
		if state.hidden[r.Line-1] {
			stateName := "removed"
			if state.folded[r.Line-1] >= 0 {
				stateName = "folded"
			}
			problems = append(problems, fmt.Errorf("replace[%d]: line %d is also %s", i, r.Line, stateName))
			continue
		}
		if r.Old == "" {
			problems = append(problems, fmt.Errorf("replace[%d]: old must not be empty", i))
			continue
		}
		if err := validateSingleLineText("old", r.Old, maxReplacementBytes); err != nil {
			problems = append(problems, fmt.Errorf("replace[%d]: %w", i, err))
			continue
		}
		if err := validateSingleLineText("new", r.New, maxReplacementBytes); err != nil {
			problems = append(problems, fmt.Errorf("replace[%d]: %w", i, err))
			continue
		}
		if r.New == r.Old {
			problems = append(problems, fmt.Errorf("replace[%d]: new must elide some part of old", i))
			continue
		}
		if !isElisionProjection(r.Old, r.New) {
			problems = append(problems, fmt.Errorf("replace[%d]: new must match all of old, with every omitted span represented by ... or …", i))
			continue
		}
		if !isHunkSource(layout.kinds[r.Line-1]) {
			problems = append(problems, fmt.Errorf("replace[%d]: line %d is not a source line inside a diff hunk", i, r.Line))
			continue
		}
		body := lines[r.Line-1].text[1:]
		if layout.python[r.Line-1] && pythonTripleStateBeforeLine(lines, layout, r.Line-1) == pythonTripleNone {
			code := trimPythonCode(body)
			if strings.HasPrefix(code, "@") || isPythonSuiteHeaderStart(code) {
				problems = append(problems, fmt.Errorf("replace[%d]: line %d is a Python decorator or suite header; keep structural anchors intact", i, r.Line))
				continue
			}
		}
		if layout.python[r.Line-1] && changesPythonBoundaryTokens(r.Old, r.New) {
			problems = append(problems, fmt.Errorf("replace[%d]: must preserve Python string and expression boundary tokens", i))
			continue
		}
		start, unique := uniqueSubstringIndex(body, r.Old)
		if !unique {
			problems = append(problems, fmt.Errorf("replace[%d]: old must occur exactly once after the diff marker on line %d", i, r.Line))
			continue
		}
		replacements[r.Line] = append(replacements[r.Line], plannedReplacement{
			lineReplacement: r,
			planIndex:       i,
			start:           start,
			end:             start + len(r.Old),
		})
	}
	for lineNo, edits := range replacements {
		sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
		for i := 1; i < len(edits); i++ {
			if edits[i].start < edits[i-1].end {
				problems = append(problems, fmt.Errorf("replace[%d]: span overlaps replace[%d] on line %d", edits[i].planIndex, edits[i-1].planIndex, lineNo))
				replacementsValid = false
			}
		}
		replacements[lineNo] = edits
	}
	if removalsValid && foldsValid && replacementsValid && len(problems) == 0 {
		if err := validateMoveReplacementSymmetry(moves, lines, state, mandatoryHidden, replacements); err != nil {
			problems = append(problems, err)
		}
	}
	if removalsValid && foldsValid && replacementsValid {
		if err := validateTripleQuoteParity(lines, layout, pythonValidationState, replacements); err != nil {
			problems = append(problems, err)
		}
		if err := validatePythonDelimiterBalance(lines, layout, pythonValidationState, replacements); err != nil {
			problems = append(problems, err)
		}
		if err := validatePythonBackslashContinuations(lines, layout, pythonValidationState, replacements); err != nil {
			problems = append(problems, err)
		}
		completeMandatoryImportFraming(layout, &state, mandatoryHidden)
		if err := validateRetainedStructure(layout, state); err != nil {
			problems = append(problems, err)
		}
	}
	if len(problems) > 0 {
		return compiledPlan{}, joinEditPlanErrors(problems)
	}

	var b strings.Builder
	b.Grow(len(raw))
	for i, line := range lines {
		lineNo := i + 1
		if foldIndex := state.foldAt[i]; foldIndex >= 0 {
			fold := state.folds[foldIndex]
			b.WriteByte(fold.marker)
			b.WriteString(fold.indent)
			b.WriteString("...")
			b.WriteString(fold.eol)
			continue
		}
		if state.hidden[i] {
			continue
		}
		text := line.text
		if edits := replacements[lineNo]; len(edits) > 0 {
			text = text[:1] + applyPlannedReplacements(text[1:], edits)
		}
		b.WriteString(text)
		b.WriteString(line.eol)
	}
	stats := computePlanStats(layout, state)
	return compiledPlan{smartDiff: b.String(), stats: stats, moves: moves}, nil
}

func changesPythonBoundaryTokens(old, new string) bool {
	for _, token := range []string{"(", ")", "[", "]", "{", "}", `'''`, `"""`} {
		if strings.Count(old, token) != strings.Count(new, token) {
			return true
		}
	}
	return false
}

func applyPlannedReplacements(body string, edits []plannedReplacement) string {
	for i := len(edits) - 1; i >= 0; i-- {
		e := edits[i]
		body = body[:e.start] + e.New + body[e.end:]
	}
	return body
}

func prepareFold(lines []sourceLine, layout diffLayout, in lineFold, planIndex int) (plannedFold, error) {
	if in.StartLine < 1 || in.EndLine < 1 || in.StartLine >= in.EndLine {
		return plannedFold{}, fmt.Errorf("fold[%d]: want at least two lines in an inclusive range, got %d-%d", planIndex, in.StartLine, in.EndLine)
	}
	if in.EndLine > len(lines) {
		return plannedFold{}, fmt.Errorf("fold[%d]: line %d is past end of diff (%d lines)", planIndex, in.EndLine, len(lines))
	}

	marker := byte(0)
	indent := ""
	haveIndent := false
	for n := in.StartLine; n <= in.EndLine; n++ {
		index := n - 1
		if !isHunkSource(layout.kinds[index]) || len(lines[index].text) == 0 {
			return plannedFold{}, fmt.Errorf("fold[%d]: line %d is not source inside one diff hunk", planIndex, n)
		}
		lineMarker := lines[index].text[0]
		if marker == 0 {
			marker = lineMarker
		} else if marker != lineMarker {
			return plannedFold{}, fmt.Errorf("fold[%d]: mixed diff markers %q and %q in range %d-%d", planIndex, marker, lineMarker, in.StartLine, in.EndLine)
		}
		body := lines[index].text[1:]
		trimmedBody := strings.TrimSpace(body)
		pythonCodeLine := layout.python[index] && pythonTripleStateBeforeLine(lines, layout, index) == pythonTripleNone
		if pythonCodeLine && lineMarker != '-' && (strings.HasPrefix(trimmedBody, "@") || isPythonSuiteHeaderStart(trimmedBody)) {
			return plannedFold{}, fmt.Errorf("fold[%d]: line %d is a Python decorator or suite owner; keep the anchor and fold only its indented interior", planIndex, n)
		}
		if trimmedBody == "" {
			continue
		}
		lineIndent := leadingWhitespace(body)
		if !haveIndent {
			indent = lineIndent
			haveIndent = true
		} else {
			indent = commonPrefix(indent, lineIndent)
		}
	}
	if !haveIndent {
		return plannedFold{}, fmt.Errorf("fold[%d]: range %d-%d contains only blank source lines", planIndex, in.StartLine, in.EndLine)
	}
	return plannedFold{
		lineFold:  in,
		planIndex: planIndex,
		marker:    marker,
		indent:    indent,
		eol:       lines[in.EndLine-1].eol,
	}, nil
}

func validateHiddenPythonOwners(lines []sourceLine, layout diffLayout, state planState) error {
	var problems []error
	for i, line := range lines {
		if !state.hidden[i] || !layout.python[i] || !isHunkSource(layout.kinds[i]) || len(line.text) < 2 || line.text[0] == '-' {
			continue
		}
		if pythonTripleStateBeforeLine(lines, layout, i) != pythonTripleNone {
			continue
		}
		body := line.text[1:]
		trimmed := trimPythonCode(body)
		indent := len(leadingWhitespace(body))
		switch {
		case strings.HasPrefix(trimmed, "@"):
			if hasPythonDefinitionAfter(lines, layout, nil, i, indent) && hasPythonDefinitionAfter(lines, layout, &state, i, indent) {
				problems = append(problems, fmt.Errorf("remove/fold: hides Python decorator on line %d while its definition remains visible", i+1))
			}
		case isPythonSuiteHeaderStart(trimmed):
			if rawEnd, ok := pythonSuiteHeaderEnd(lines, layout, nil, i); ok && hasPythonBodyAfter(lines, layout, nil, rawEnd, indent) && hasPythonBodyAfter(lines, layout, &state, rawEnd, indent) {
				problems = append(problems, fmt.Errorf("remove/fold: hides Python suite owner on line %d while its body remains visible", i+1))
			}
		}
	}
	if len(problems) > 0 {
		return joinEditPlanErrors(problems)
	}
	return nil
}

func hunkHasVisibleChange(layout diffLayout, state planState, start, end int) bool {
	for i := start; i < end; i++ {
		if layout.kinds[i] == diffLineHunkChange && state.represented(i) {
			return true
		}
	}
	return false
}

func validateHiddenPythonBoundaries(lines []sourceLine, layout diffLayout, state planState) error {
	var problems []error
	for hunk, kind := range layout.kinds {
		if kind != diffLineHunkHeader || !layout.python[hunk] {
			continue
		}
		end := nextLayoutLine(layout, hunk+1, func(k diffLineKind) bool {
			return k == diffLineHeader || k == diffLineOldFile || k == diffLineHunkHeader || k == diffLineMailSignature
		})
		if !hunkHasVisibleChange(layout, state, hunk+1, end) {
			continue
		}
		for i := hunk + 1; i < end; {
			if !state.hidden[i] || !isHunkSource(layout.kinds[i]) {
				i++
				continue
			}
			start := i
			for i < end && state.hidden[i] && isHunkSource(layout.kinds[i]) {
				i++
			}
			for _, side := range []byte{'-', '+'} {
				if !hiddenPythonRegionBalanced(lines, layout, hunk+1, start, i, side) {
					sideName := "old"
					if side == '+' {
						sideName = "new"
					}
					problems = append(problems, fmt.Errorf("remove/fold: hidden Python region %d-%d crosses %s-side expression or string boundaries; keep the boundaries and compress only their interior", start+1, i, sideName))
				}
			}
		}
	}
	if len(problems) > 0 {
		return joinEditPlanErrors(problems)
	}
	return nil
}

func hiddenPythonRegionBalanced(lines []sourceLine, layout diffLayout, hunkStart, start, end int, side byte) bool {
	var beforeTriple pythonTripleState
	for i := hunkStart; i < start; i++ {
		if pythonLineOnSide(lines, layout, i, side) {
			scanPythonTripleLine(lines[i].text[1:], &beforeTriple)
		}
	}
	enteredTriple := beforeTriple != pythonTripleNone
	actualTriple := beforeTriple
	localTriple := pythonTripleNone
	tripleTransitions := 0
	var bodies []string
	for i := start; i < end; i++ {
		if !pythonLineOnSide(lines, layout, i, side) {
			continue
		}
		body := lines[i].text[1:]
		bodies = append(bodies, body)
		scanPythonTripleLine(body, &localTriple)
		if enteredTriple {
			tripleTransitions += scanPythonTripleLine(body, &actualTriple)
		}
	}
	if len(bodies) == 0 {
		return true
	}
	trimmedFirst := strings.TrimSpace(bodies[0])
	startsWithBareTriple := trimmedFirst == `"""` || trimmedFirst == `'''`
	if enteredTriple && tripleTransitions > 0 && (localTriple != pythonTripleNone || startsWithBareTriple) {
		return false
	}
	if !enteredTriple && localTriple != pythonTripleNone {
		return false
	}

	delimiterTriple := pythonTripleNone
	if enteredTriple && tripleTransitions == 0 {
		delimiterTriple = beforeTriple
	}
	var balance pythonDelimiters
	for _, body := range bodies {
		balance = balance.add(pythonDelimiterBalanceWithState(body, &delimiterTriple))
		if balance.round < 0 || balance.square < 0 || balance.curly < 0 {
			return false
		}
	}
	return balance == (pythonDelimiters{})
}

func pythonLineOnSide(lines []sourceLine, layout diffLayout, line int, side byte) bool {
	if !isHunkSource(layout.kinds[line]) || len(lines[line].text) < 2 {
		return false
	}
	return lines[line].text[0] == ' ' || lines[line].text[0] == side
}

func pythonNewSideStates(lines []sourceLine, layout diffLayout) (insideTriple []bool, depthBefore []int) {
	insideTriple = make([]bool, len(lines))
	depthBefore = make([]int, len(lines))
	for hunk, kind := range layout.kinds {
		if kind != diffLineHunkHeader || !layout.python[hunk] {
			continue
		}
		end := nextLayoutLine(layout, hunk+1, func(k diffLineKind) bool {
			return k == diffLineHeader || k == diffLineOldFile || k == diffLineHunkHeader || k == diffLineMailSignature
		})
		var triple pythonTripleState
		var delimiters pythonDelimiters
		for i := hunk + 1; i < end; i++ {
			if !isHunkSource(layout.kinds[i]) || len(lines[i].text) < 2 || lines[i].text[0] == '-' {
				continue
			}
			insideTriple[i] = triple != pythonTripleNone
			depthBefore[i] = delimiters.round + delimiters.square + delimiters.curly
			delimiters = delimiters.add(pythonDelimiterBalanceWithState(lines[i].text[1:], &triple))
			// A hunk can start in the middle of an expression, with context
			// that closes delimiters opened above the hunk. Those unmatched
			// closers describe invisible source, not negative nesting for the
			// rest of this hunk. Clamp each delimiter kind independently so a
			// later top-level assignment still receives reference protection.
			if delimiters.round < 0 {
				delimiters.round = 0
			}
			if delimiters.square < 0 {
				delimiters.square = 0
			}
			if delimiters.curly < 0 {
				delimiters.curly = 0
			}
		}
	}
	return insideTriple, depthBefore
}

func validateHiddenReferences(lines []sourceLine, layout diffLayout, state planState) error {
	insideTriple, depthBefore := pythonNewSideStates(lines, layout)
	var problems []error
	for i, line := range lines {
		if !state.hidden[i] || !layout.python[i] || !isHunkSource(layout.kinds[i]) || len(line.text) < 2 || line.text[0] == '-' {
			continue
		}
		if insideTriple[i] || depthBefore[i] != 0 {
			continue
		}
		name, ok := simpleAssignedReference(pythonCodeWithoutStrings(line.text[1:]))
		if !ok {
			continue
		}
		fileID := layout.fileID[i]
		for j := 0; j < len(lines); j++ {
			if layout.fileID[j] != fileID || state.hidden[j] || !isHunkSource(layout.kinds[j]) || len(lines[j].text) < 2 {
				continue
			}
			if line.text[0] == '+' && lines[j].text[0] == '-' {
				continue
			}
			if insideTriple[j] {
				continue
			}
			body := pythonCodeWithoutStrings(lines[j].text[1:])
			if !containsIdentifier(body, name) {
				continue
			}
			if foldIndex := state.folded[i]; foldIndex >= 0 {
				problems = append(problems, fmt.Errorf("fold[%d]: hides definition %q on line %d while retained line %d still references it; keep the definition and fold only its interior", foldIndex, name, i+1, j+1))
			} else {
				problems = append(problems, fmt.Errorf("remove: hides definition %q on line %d while retained line %d still references it; keep the definition and compress only its interior", name, i+1, j+1))
			}
			break
		}
	}
	if len(problems) > 0 {
		return joinEditPlanErrors(problems)
	}
	return nil
}

func validatePythonSuiteSkeleton(lines []sourceLine, layout diffLayout, state planState) error {
	var problems []error
	for i, line := range lines {
		if !layout.python[i] || state.hidden[i] || !isHunkSource(layout.kinds[i]) || len(line.text) < 2 || line.text[0] == '-' {
			continue
		}
		if pythonTripleStateBeforeLine(lines, layout, i) != pythonTripleNone {
			continue
		}
		body := line.text[1:]
		trimmed := trimPythonCode(body)
		if strings.HasPrefix(trimmed, "@") {
			indent := len(leadingWhitespace(body))
			if hasPythonDefinitionAfter(lines, layout, nil, i, indent) && !hasPythonDefinitionAfter(lines, layout, &state, i, indent) {
				problems = append(problems, fmt.Errorf("remove/fold: retained Python decorator on line %d has no attached definition", i+1))
			}
			continue
		}
		if !isPythonSuiteHeaderStart(trimmed) {
			continue
		}
		rawEnd, rawOK := pythonSuiteHeaderEnd(lines, layout, nil, i)
		if !rawOK {
			continue
		}
		ownerIndent := len(leadingWhitespace(body))
		if hasPythonBodyAfter(lines, layout, nil, rawEnd, ownerIndent) {
			retainedEnd, retainedOK := pythonSuiteHeaderEnd(lines, layout, &state, i)
			if !retainedOK || !hasPythonBodyAfter(lines, layout, &state, retainedEnd, ownerIndent) {
				problems = append(problems, fmt.Errorf("remove/fold: retained Python suite owner on line %d has no indented body; keep a semantic body line or an interior fold", i+1))
			}
		}
	}
	if len(problems) > 0 {
		return joinEditPlanErrors(problems)
	}
	return nil
}

func pythonSuiteHeaderEnd(lines []sourceLine, layout diffLayout, state *planState, start int) (int, bool) {
	_, hunkEnd := containingHunk(layout, start)
	depth := 0
	for i := start; i < hunkEnd; i++ {
		if state != nil {
			if state.foldAt[i] >= 0 {
				continue
			}
			if state.hidden[i] {
				continue
			}
		}
		if !isHunkSource(layout.kinds[i]) || len(lines[i].text) < 2 || lines[i].text[0] == '-' {
			continue
		}
		body := lines[i].text[1:]
		trimmed := trimPythonCode(body)
		if trimmed == "" {
			continue
		}
		depth += pythonDelimiterDepth(body)
		if depth < 0 {
			return 0, false
		}
		if depth == 0 && strings.HasSuffix(trimmed, ":") {
			return i, true
		}
	}
	return 0, false
}

func hasPythonBodyAfter(lines []sourceLine, layout diffLayout, state *planState, ownerLine, indent int) bool {
	_, hunkEnd := containingHunk(layout, ownerLine)
	for i := ownerLine + 1; i < hunkEnd; i++ {
		if state != nil {
			if foldIndex := state.foldAt[i]; foldIndex >= 0 {
				fold := state.folds[foldIndex]
				if fold.marker == '-' {
					continue
				}
				return (fold.marker == '+' || fold.marker == ' ') && len(fold.indent) > indent
			}
			if state.hidden[i] {
				continue
			}
		}
		if !isHunkSource(layout.kinds[i]) || len(lines[i].text) < 2 || lines[i].text[0] == '-' {
			continue
		}
		body := lines[i].text[1:]
		if trimPythonCode(body) == "" {
			continue
		}
		return len(leadingWhitespace(body)) > indent
	}
	return false
}

func hasPythonDefinitionAfter(lines []sourceLine, layout diffLayout, state *planState, decoratorLine, indent int) bool {
	_, hunkEnd := containingHunk(layout, decoratorLine)
	depth := pythonDelimiterDepth(lines[decoratorLine].text[1:])
	for i := decoratorLine + 1; i < hunkEnd; i++ {
		if state != nil {
			if state.foldAt[i] >= 0 {
				continue
			}
			if state.hidden[i] {
				continue
			}
		}
		if !isHunkSource(layout.kinds[i]) || len(lines[i].text) < 2 || lines[i].text[0] == '-' {
			continue
		}
		body := lines[i].text[1:]
		trimmed := trimPythonCode(body)
		if trimmed == "" {
			continue
		}
		if depth > 0 {
			depth += pythonDelimiterDepth(body)
			if depth < 0 {
				return false
			}
			continue
		}
		if len(leadingWhitespace(body)) != indent {
			return false
		}
		if strings.HasPrefix(trimmed, "@") {
			depth = pythonDelimiterDepth(body)
			continue
		}
		return isPythonDefinition(trimmed)
	}
	return false
}

func isPythonDefinition(trimmed string) bool {
	trimmed = trimPythonCode(trimmed)
	return strings.HasPrefix(trimmed, "def ") || strings.HasPrefix(trimmed, "async def ") || strings.HasPrefix(trimmed, "class ")
}

type pythonDelimiters struct {
	round  int
	square int
	curly  int
}

func (d pythonDelimiters) add(other pythonDelimiters) pythonDelimiters {
	return pythonDelimiters{d.round + other.round, d.square + other.square, d.curly + other.curly}
}

func (d pythonDelimiters) equal(other pythonDelimiters) bool {
	return d == other
}

func pythonDelimiterDepth(text string) int {
	d := pythonDelimiterBalance(text)
	return d.round + d.square + d.curly
}

func pythonDelimiterBalance(text string) pythonDelimiters {
	state := pythonTripleNone
	return pythonDelimiterBalanceWithState(text, &state)
}

func pythonDelimiterBalanceWithState(text string, state *pythonTripleState) pythonDelimiters {
	var balance pythonDelimiters
	for i := 0; i < len(text); {
		if *state != pythonTripleNone {
			delim := `'''`
			if *state == pythonTripleDouble {
				delim = `"""`
			}
			at := strings.Index(text[i:], delim)
			if at < 0 {
				return balance
			}
			i += at + len(delim)
			*state = pythonTripleNone
			continue
		}
		b := text[i]
		if b == '#' {
			break
		}
		if strings.HasPrefix(text[i:], `'''`) {
			*state = pythonTripleSingle
			i += 3
			continue
		}
		if strings.HasPrefix(text[i:], `"""`) {
			*state = pythonTripleDouble
			i += 3
			continue
		}
		if b == '\'' || b == '"' {
			quote := b
			i++
			for i < len(text) {
				if text[i] == '\\' {
					i += 2
					continue
				}
				if text[i] == quote {
					i++
					break
				}
				i++
			}
			continue
		}
		switch b {
		case '(':
			balance.round++
		case ')':
			balance.round--
		case '[':
			balance.square++
		case ']':
			balance.square--
		case '{':
			balance.curly++
		case '}':
			balance.curly--
		}
		i++
	}
	return balance
}

func validatePythonDelimiterBalance(lines []sourceLine, layout diffLayout, state planState, replacements map[int][]plannedReplacement) error {
	var problems []error
	for i, kind := range layout.kinds {
		if kind != diffLineHunkHeader || !layout.python[i] {
			continue
		}
		end := nextLayoutLine(layout, i+1, func(k diffLineKind) bool {
			return k == diffLineHeader || k == diffLineOldFile || k == diffLineHunkHeader || k == diffLineMailSignature
		})
		if !hunkHasVisibleChange(layout, state, i+1, end) {
			continue
		}
		var rawOld, rawNew, keptOld, keptNew pythonDelimiters
		var rawOldTriple, rawNewTriple, keptOldTriple, keptNewTriple pythonTripleState
		for j := i + 1; j < end; j++ {
			if !isHunkSource(layout.kinds[j]) || len(lines[j].text) < 1 {
				continue
			}
			body := lines[j].text[1:]
			keptBody := body
			if edits := replacements[j+1]; len(edits) > 0 {
				keptBody = applyPlannedReplacements(body, edits)
			}
			switch lines[j].text[0] {
			case ' ':
				rawOld = rawOld.add(pythonDelimiterBalanceWithState(body, &rawOldTriple))
				rawNew = rawNew.add(pythonDelimiterBalanceWithState(body, &rawNewTriple))
				if !state.hidden[j] {
					keptOld = keptOld.add(pythonDelimiterBalanceWithState(keptBody, &keptOldTriple))
					keptNew = keptNew.add(pythonDelimiterBalanceWithState(keptBody, &keptNewTriple))
				}
			case '-':
				rawOld = rawOld.add(pythonDelimiterBalanceWithState(body, &rawOldTriple))
				if !state.hidden[j] {
					keptOld = keptOld.add(pythonDelimiterBalanceWithState(keptBody, &keptOldTriple))
				}
			case '+':
				rawNew = rawNew.add(pythonDelimiterBalanceWithState(body, &rawNewTriple))
				if !state.hidden[j] {
					keptNew = keptNew.add(pythonDelimiterBalanceWithState(keptBody, &keptNewTriple))
				}
			}
		}
		if !rawOld.equal(keptOld) || !rawNew.equal(keptNew) {
			problems = append(problems, fmt.Errorf("remove/fold: hunk on line %d must preserve Python (), [], and {} delimiter balance; keep both boundaries or fold/remove the complete balanced expression", i+1))
		}
	}
	if len(problems) > 0 {
		return joinEditPlanErrors(problems)
	}
	return nil
}

func validatePythonBackslashContinuations(lines []sourceLine, layout diffLayout, state planState, replacements map[int][]plannedReplacement) error {
	var problems []error
	for hunk, kind := range layout.kinds {
		if kind != diffLineHunkHeader || !layout.python[hunk] {
			continue
		}
		end := nextLayoutLine(layout, hunk+1, func(k diffLineKind) bool {
			return k == diffLineHeader || k == diffLineOldFile || k == diffLineHunkHeader || k == diffLineMailSignature
		})
		if !hunkHasVisibleChange(layout, state, hunk+1, end) {
			continue
		}
		for _, side := range []byte{'-', '+'} {
			var sideLines []int
			for i := hunk + 1; i < end; i++ {
				if pythonLineOnSide(lines, layout, i, side) {
					sideLines = append(sideLines, i)
				}
			}
			var triple pythonTripleState
			for i := 0; i+1 < len(sideLines); i++ {
				line := sideLines[i]
				next := sideLines[i+1]
				body := lines[line].text[1:]
				insideTriple := triple != pythonTripleNone
				scanPythonTripleLine(body, &triple)
				if insideTriple || !endsPythonBackslash(body) {
					continue
				}
				lineHidden := state.hidden[line]
				nextHidden := state.hidden[next]
				if !lineHidden {
					keptBody := body
					if edits := replacements[line+1]; len(edits) > 0 {
						keptBody = applyPlannedReplacements(body, edits)
					}
					if !endsPythonBackslash(keptBody) {
						continue
					}
				}
				if lineHidden != nextHidden {
					problems = append(problems, fmt.Errorf("remove/fold: Python backslash continuation on lines %d-%d must be retained or hidden together", line+1, next+1))
				}
			}
		}
	}
	if len(problems) > 0 {
		return joinEditPlanErrors(problems)
	}
	return nil
}

func endsPythonBackslash(body string) bool {
	code := trimPythonCode(body)
	count := 0
	for i := len(code) - 1; i >= 0 && code[i] == '\\'; i-- {
		count++
	}
	return count%2 == 1
}

type pythonTripleState uint8

const (
	pythonTripleNone pythonTripleState = iota
	pythonTripleSingle
	pythonTripleDouble
)

func scanPythonTripleLine(text string, state *pythonTripleState) int {
	transitions := 0
	for i := 0; i < len(text); {
		if *state != pythonTripleNone {
			delim := `'''`
			if *state == pythonTripleDouble {
				delim = `"""`
			}
			at := strings.Index(text[i:], delim)
			if at < 0 {
				return transitions
			}
			i += at + len(delim)
			*state = pythonTripleNone
			transitions++
			continue
		}
		if text[i] == '#' {
			return transitions
		}
		if strings.HasPrefix(text[i:], `'''`) {
			*state = pythonTripleSingle
			transitions++
			i += 3
			continue
		}
		if strings.HasPrefix(text[i:], `"""`) {
			*state = pythonTripleDouble
			transitions++
			i += 3
			continue
		}
		if text[i] == '\'' || text[i] == '"' {
			quote := text[i]
			i++
			for i < len(text) {
				if text[i] == '\\' {
					i += 2
					continue
				}
				if text[i] == quote {
					i++
					break
				}
				i++
			}
			continue
		}
		i++
	}
	return transitions
}

func validateTripleQuoteParity(lines []sourceLine, layout diffLayout, state planState, replacements map[int][]plannedReplacement) error {
	var problems []error
	for i, kind := range layout.kinds {
		if kind != diffLineHunkHeader || !layout.python[i] {
			continue
		}
		end := nextLayoutLine(layout, i+1, func(k diffLineKind) bool {
			return k == diffLineHeader || k == diffLineOldFile || k == diffLineHunkHeader || k == diffLineMailSignature
		})
		if !hunkHasVisibleChange(layout, state, i+1, end) {
			continue
		}
		var rawOld, rawNew, keptOld, keptNew pythonTripleState
		for j := i + 1; j < end; j++ {
			if !isHunkSource(layout.kinds[j]) || len(lines[j].text) < 1 {
				continue
			}
			body := lines[j].text[1:]
			keptBody := body
			if edits := replacements[j+1]; len(edits) > 0 {
				keptBody = applyPlannedReplacements(body, edits)
			}
			switch lines[j].text[0] {
			case ' ':
				scanPythonTripleLine(body, &rawOld)
				scanPythonTripleLine(body, &rawNew)
				if !state.hidden[j] {
					scanPythonTripleLine(keptBody, &keptOld)
					scanPythonTripleLine(keptBody, &keptNew)
				}
			case '-':
				scanPythonTripleLine(body, &rawOld)
				if !state.hidden[j] {
					scanPythonTripleLine(keptBody, &keptOld)
				}
			case '+':
				scanPythonTripleLine(body, &rawNew)
				if !state.hidden[j] {
					scanPythonTripleLine(keptBody, &keptNew)
				}
			}
		}
		if rawOld != keptOld || rawNew != keptNew {
			problems = append(problems, fmt.Errorf("remove/fold: hunk on line %d must preserve balanced Python triple-quote boundaries; fold or remove the complete string, or keep both boundaries", i+1))
		}
	}
	if len(problems) > 0 {
		return joinEditPlanErrors(problems)
	}
	return nil
}

func pythonCodeWithoutStrings(text string) string {
	var b strings.Builder
	for i := 0; i < len(text); {
		if text[i] == '#' {
			break
		}
		if strings.HasPrefix(text[i:], `'''`) || strings.HasPrefix(text[i:], `"""`) {
			break
		}
		if text[i] == '\'' || text[i] == '"' {
			quote := text[i]
			i++
			for i < len(text) {
				if text[i] == '\\' {
					i += 2
					continue
				}
				if text[i] == quote {
					i++
					break
				}
				i++
			}
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	return b.String()
}

func trimPythonCode(text string) string {
	var quote byte
	escaped := false
	for i := 0; i < len(text); i++ {
		b := text[i]
		if quote != 0 {
			if escaped {
				escaped = false
				continue
			}
			if b == '\\' {
				escaped = true
				continue
			}
			if b == quote {
				quote = 0
			}
			continue
		}
		if b == '\'' || b == '"' {
			quote = b
			continue
		}
		if b == '#' {
			return strings.TrimSpace(text[:i])
		}
	}
	return strings.TrimSpace(text)
}

func isPythonSuiteHeaderStart(trimmed string) bool {
	trimmed = trimPythonCode(trimmed)
	if isPythonDefinition(trimmed) {
		return true
	}
	for _, keyword := range []string{"if", "elif", "for", "while", "with", "except", "except*", "match", "case"} {
		if hasPythonKeyword(trimmed, keyword) {
			return true
		}
	}
	for _, keyword := range []string{"async for", "async with"} {
		if strings.HasPrefix(trimmed, keyword+" ") || strings.HasPrefix(trimmed, keyword+"(") {
			return true
		}
	}
	switch trimmed {
	case "else:", "try:", "except:", "finally:":
		return true
	default:
		return false
	}
}

func hasPythonKeyword(text, keyword string) bool {
	if !strings.HasPrefix(text, keyword) || len(text) == len(keyword) {
		return false
	}
	next := text[len(keyword)]
	return next == ' ' || next == '\t' || next == '('
}

func isPythonSuiteOwner(trimmed string) bool {
	trimmed = trimPythonCode(trimmed)
	return isPythonSuiteHeaderStart(trimmed) && strings.HasSuffix(trimmed, ":")
}

func containingHunk(layout diffLayout, line int) (start, end int) {
	start = line
	for start > 0 && layout.kinds[start] != diffLineHunkHeader {
		start--
	}
	if layout.kinds[start] == diffLineHunkHeader {
		start++
	}
	end = line + 1
	for end < len(layout.kinds) {
		kind := layout.kinds[end]
		if kind == diffLineHunkHeader || kind == diffLineHeader || kind == diffLineOldFile || kind == diffLineMailSignature {
			break
		}
		end++
	}
	return start, end
}

func pythonTripleStateBeforeLine(lines []sourceLine, layout diffLayout, line int) pythonTripleState {
	hunkStart, _ := containingHunk(layout, line)
	var state pythonTripleState
	for i := hunkStart; i < line; i++ {
		if !isHunkSource(layout.kinds[i]) || len(lines[i].text) < 2 || lines[i].text[0] == '-' {
			continue
		}
		scanPythonTripleLine(lines[i].text[1:], &state)
	}
	return state
}

func simpleAssignedReference(body string) (string, bool) {
	trimmed := strings.TrimSpace(body)
	eq := strings.IndexByte(trimmed, '=')
	if eq <= 0 || eq+1 < len(trimmed) && trimmed[eq+1] == '=' {
		return "", false
	}
	lhs := strings.TrimSpace(trimmed[:eq])
	if colon := strings.IndexByte(lhs, ':'); colon >= 0 {
		lhs = strings.TrimSpace(lhs[:colon])
	}
	if lhs == "" {
		return "", false
	}
	for _, segment := range strings.Split(lhs, ".") {
		if segment == "" || !isIdentifierStart(segment[0]) {
			return "", false
		}
		for i := 1; i < len(segment); i++ {
			if !isIdentifierContinue(segment[i]) {
				return "", false
			}
		}
	}
	return lhs, true
}

func containsIdentifier(text, name string) bool {
	for offset := 0; offset <= len(text)-len(name); {
		at := strings.Index(text[offset:], name)
		if at < 0 {
			return false
		}
		at += offset
		beforeOK := at == 0 || !isIdentifierContinue(text[at-1])
		after := at + len(name)
		afterOK := after == len(text) || !isIdentifierContinue(text[after])
		if beforeOK && afterOK {
			return true
		}
		offset = at + 1
	}
	return false
}

func isIdentifierStart(b byte) bool {
	return b == '_' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z'
}

func isIdentifierContinue(b byte) bool {
	return isIdentifierStart(b) || b >= '0' && b <= '9'
}

func leadingWhitespace(s string) string {
	for i, r := range s {
		if r != ' ' && r != '\t' {
			return s[:i]
		}
	}
	return s
}

func commonPrefix(a, b string) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	i := 0
	for i < n && a[i] == b[i] {
		i++
	}
	return a[:i]
}

func computePlanStats(layout diffLayout, state planState) planStats {
	var stats planStats
	for i, kind := range layout.kinds {
		switch kind {
		case diffLineHeader:
			stats.rawFiles++
			if state.represented(i) {
				stats.visibleFiles++
			}
		case diffLineHunkChange:
			stats.rawChanged++
			switch {
			case state.folded[i] >= 0:
				stats.foldedChanged++
			case state.hidden[i]:
				stats.removedChanged++
			default:
				stats.visibleChanged++
			}
		}
	}
	stats.foldCount = len(state.folds)
	for _, fold := range state.folds {
		if fold.marker == '+' || fold.marker == '-' {
			stats.visibleChanged++
		}
	}
	return stats
}

func joinEditPlanErrors(problems []error) error {
	if len(problems) == 1 {
		return problems[0]
	}
	var b strings.Builder
	for _, err := range problems {
		fmt.Fprintf(&b, "\n- %v", err)
	}
	return fmt.Errorf("edit plan has %d errors:%s", len(problems), b.String())
}

func uniqueSubstringIndex(text, sub string) (int, bool) {
	first := strings.Index(text, sub)
	if first < 0 {
		return 0, false
	}
	// Search one byte after the first start, rather than after its end, so
	// overlapping matches ("aa" in "aaa") are also considered ambiguous.
	if strings.Index(text[first+1:], sub) >= 0 {
		return 0, false
	}
	return first, true
}

func isElisionProjection(old, new string) bool {
	return matchesElisionProjection(old, new, true)
}

// isComposedElisionProjection recognizes the result after multiple individually
// validated replacements were applied to one line. Adjacent placeholders (or a
// placeholder next to source dots) may form a run longer than three dots.
func isComposedElisionProjection(old, new string) bool {
	return matchesElisionProjection(old, new, false)
}

func matchesElisionProjection(old, new string, strict bool) bool {
	re, ok := compileElisionProjection(new, strict)
	return ok && re.MatchString(old)
}

func compileElisionProjection(new string, strict bool) (*regexp.Regexp, bool) {
	newRunes := []rune(new)
	var pattern strings.Builder
	pattern.WriteByte('^')
	var literal strings.Builder
	wildcards := 0
	lastWasWildcard := false
	flushLiteral := func() {
		if literal.Len() == 0 {
			return
		}
		pattern.WriteString(regexp.QuoteMeta(literal.String()))
		literal.Reset()
	}

	for i := 0; i < len(newRunes); {
		wildcard := false
		switch {
		case newRunes[i] == '…':
			wildcard = true
			i++
		case newRunes[i] == '.':
			j := i
			for j < len(newRunes) && newRunes[j] == '.' {
				j++
			}
			run := j - i
			if run >= 3 {
				if strict && run != 3 {
					return nil, false
				}
				wildcard = true
				i = j
			}
		}
		if wildcard {
			if lastWasWildcard {
				if strict {
					return nil, false
				}
				continue
			}
			flushLiteral()
			// Every visible ellipsis must stand for at least one omitted
			// character. This prevents silent changes such as !allowed → allowed.
			pattern.WriteString(".+")
			wildcards++
			lastWasWildcard = true
			continue
		}
		literal.WriteRune(newRunes[i])
		lastWasWildcard = false
		i++
	}
	flushLiteral()
	pattern.WriteByte('$')
	if wildcards == 0 {
		return nil, false
	}
	re, err := regexp.Compile(pattern.String())
	return re, err == nil
}

func isHunkSource(kind diffLineKind) bool {
	return kind == diffLineHunkContext || kind == diffLineHunkChange
}

func validateSummary(summary string) error {
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("summary must not be empty")
	}
	if len(summary) > maxSummaryBytes {
		return fmt.Errorf("summary is %d bytes, over the %d-byte limit", len(summary), maxSummaryBytes)
	}
	if err := validateSingleLineText("summary", summary, maxSummaryBytes); err != nil {
		return err
	}
	return nil
}

func validateSingleLineText(name, text string, maxBytes int) error {
	if len(text) > maxBytes {
		return fmt.Errorf("%s is %d bytes, over the %d-byte limit", name, len(text), maxBytes)
	}
	if !utf8.ValidString(text) {
		return fmt.Errorf("%s is not valid UTF-8", name)
	}
	for _, r := range text {
		switch r {
		case '\n', '\r', 0, '\x1b', '\u2028', '\u2029':
			return fmt.Errorf("%s must be a single printable line", name)
		}
		if unicode.IsControl(r) && r != '\t' {
			return fmt.Errorf("%s contains a control character", name)
		}
	}
	return nil
}

// analyzeDiff classifies structural and source lines. Parsed hunk counts are
// important: source such as "-- counter" appears in a diff as "--- counter"
// and must not be mistaken for a file header while the hunk still has lines.
func analyzeDiff(lines []sourceLine) diffLayout {
	layout := diffLayout{
		kinds:       make([]diffLineKind, len(lines)),
		markerOwner: make([]int, len(lines)),
		python:      make([]bool, len(lines)),
		language:    make([]sourceLanguage, len(lines)),
		fileID:      make([]int, len(lines)),
		hunkID:      make([]int, len(lines)),
	}
	for i := range layout.markerOwner {
		layout.markerOwner[i] = -1
		layout.fileID[i] = -1
		layout.hunkID[i] = -1
	}

	var inHunk, countsKnown, hunkProblemReported bool
	var oldRemain, newRemain, hunkHeaderLine int
	var currentLanguage sourceLanguage
	currentFileID := -1
	currentHunkID := -1
	gitFileSection := false
	inFileSection := false
	reportHunkProblem := func(atLine int) {
		if hunkProblemReported {
			return
		}
		layout.problems = append(layout.problems, fmt.Errorf(
			"hunk header on line %d has counts inconsistent with its body near line %d",
			hunkHeaderLine+1, atLine+1,
		))
		hunkProblemReported = true
	}

	for i := 0; i < len(lines); {
		text := lines[i].text
		if !inHunk {
			switch {
			case isGitDiffHeader(text):
				currentFileID++
				gitFileSection = true
				inFileSection = true
				currentLanguage = diffHeaderLanguage(text)
			case isRawOldFileHeader(lines, i):
				if !gitFileSection {
					currentFileID++
				}
				inFileSection = true
				if oldLanguage := fileMarkerLanguage(lines[i].text); oldLanguage != sourceLanguageUnknown {
					currentLanguage = oldLanguage
				}
				if newLanguage := fileMarkerLanguage(lines[i+1].text); newLanguage != sourceLanguageUnknown {
					currentLanguage = newLanguage
				}
			}
		}
		layout.language[i] = currentLanguage
		layout.python[i] = currentLanguage == sourceLanguagePython
		layout.fileID[i] = currentFileID
		if inHunk {
			if !countsKnown && isRawOldFileHeader(lines, i) {
				// A bare/otherwise unparseable @@ header has no counts. Recover at
				// an explicit paired file header rather than making it editable.
				inHunk = false
				continue
			}
			if countsKnown && oldRemain == 0 && newRemain == 0 {
				if isNoNewlineMarker(text) {
					layout.kinds[i] = diffLineNoNewline
					layout.hunkID[i] = currentHunkID
					if i > 0 && isHunkSource(layout.kinds[i-1]) {
						layout.markerOwner[i] = i - 1
					}
					i++
					continue
				}
				if isRawOldFileHeader(lines, i) || isGitDiffHeader(text) || strings.HasPrefix(text, "@@") || isFormatPatchSignature(text) {
					inHunk = false
					continue
				}
				if kind := hunkSourceKind(text); kind != diffLineOther {
					reportHunkProblem(i)
					layout.kinds[i] = kind
					layout.hunkID[i] = currentHunkID
					i++
					continue
				}
				inHunk = false
				continue
			}
			if isNoNewlineMarker(text) {
				layout.kinds[i] = diffLineNoNewline
				layout.hunkID[i] = currentHunkID
				if i > 0 && isHunkSource(layout.kinds[i-1]) {
					layout.markerOwner[i] = i - 1
				}
				i++
				continue
			}

			kind := hunkSourceKind(text)
			if kind != diffLineOther {
				if countsKnown {
					valid := false
					switch text[0] {
					case ' ':
						valid = oldRemain > 0 && newRemain > 0
						if valid {
							oldRemain--
							newRemain--
						}
					case '-':
						valid = oldRemain > 0
						if valid {
							oldRemain--
						}
					case '+':
						valid = newRemain > 0
						if valid {
							newRemain--
						}
					}
					if !valid {
						reportHunkProblem(i)
					}
				}
				layout.kinds[i] = kind
				layout.hunkID[i] = currentHunkID
				i++
				continue
			}

			if countsKnown && (oldRemain != 0 || newRemain != 0) {
				reportHunkProblem(i)
			}
			// Re-run this line through the outer structural classifier.
			inHunk = false
			continue
		}

		switch {
		case isFormatPatchSignature(text):
			layout.kinds[i] = diffLineMailSignature
			inFileSection = false
			gitFileSection = false
			i++
		case isGitDiffHeader(text):
			layout.kinds[i] = diffLineHeader
			i++
		case inFileSection && (strings.HasPrefix(text, "index ") || strings.HasPrefix(text, "similarity index ") || strings.HasPrefix(text, "dissimilarity index ")):
			layout.kinds[i] = diffLineIndex
			i++
		case inFileSection && strings.HasPrefix(text, "rename from "):
			layout.kinds[i] = diffLineRenameFrom
			i++
		case inFileSection && strings.HasPrefix(text, "rename to "):
			layout.kinds[i] = diffLineRenameTo
			i++
		case inFileSection && strings.HasPrefix(text, "copy from "):
			layout.kinds[i] = diffLineCopyFrom
			i++
		case inFileSection && strings.HasPrefix(text, "copy to "):
			layout.kinds[i] = diffLineCopyTo
			i++
		case isRawOldFileHeader(lines, i):
			layout.kinds[i] = diffLineOldFile
			layout.kinds[i+1] = diffLineNewFile
			layout.language[i+1] = currentLanguage
			layout.python[i+1] = currentLanguage == sourceLanguagePython
			layout.fileID[i+1] = currentFileID
			i += 2
		case (inFileSection || text == "@@" || strings.HasPrefix(text, "@@ ")) && strings.HasPrefix(text, "@@"):
			layout.kinds[i] = diffLineHunkHeader
			currentHunkID++
			layout.hunkID[i] = currentHunkID
			oldRemain, newRemain, countsKnown = parseHunkCounts(text)
			inHunk = true
			hunkHeaderLine = i
			hunkProblemReported = false
			i++
		default:
			i++
		}
	}
	if inHunk && countsKnown && (oldRemain != 0 || newRemain != 0) {
		reportHunkProblem(len(lines) - 1)
	}
	return layout
}

func hunkSourceKind(text string) diffLineKind {
	if text == "" {
		return diffLineOther
	}
	switch text[0] {
	case ' ':
		return diffLineHunkContext
	case '+', '-':
		return diffLineHunkChange
	default:
		return diffLineOther
	}
}

func parseHunkCounts(header string) (oldCount, newCount int, ok bool) {
	fields := strings.Fields(header)
	if len(fields) < 4 || fields[0] != "@@" || fields[3] != "@@" {
		return 0, 0, false
	}
	oldCount, ok = parseHunkRange(fields[1], '-')
	if !ok {
		return 0, 0, false
	}
	newCount, ok = parseHunkRange(fields[2], '+')
	return oldCount, newCount, ok
}

func parseHunkRange(field string, sign byte) (int, bool) {
	if len(field) < 2 || field[0] != sign {
		return 0, false
	}
	rangeText := field[1:]
	count := 1
	if comma := strings.IndexByte(rangeText, ','); comma >= 0 {
		var err error
		count, err = strconv.Atoi(rangeText[comma+1:])
		if err != nil || count < 0 {
			return 0, false
		}
		rangeText = rangeText[:comma]
	}
	if _, err := strconv.Atoi(rangeText); err != nil {
		return 0, false
	}
	return count, true
}

func diffHeaderLanguage(line string) sourceLanguage {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return sourceLanguageUnknown
	}
	var language sourceLanguage
	for _, field := range fields[2:] {
		candidate := pathLanguage(field)
		if candidate != sourceLanguageUnknown {
			language = candidate
		}
	}
	return language
}

func fileMarkerLanguage(line string) sourceLanguage {
	if len(line) < 4 {
		return sourceLanguageUnknown
	}
	path := strings.TrimSpace(line[4:])
	if tab := strings.IndexByte(path, '\t'); tab >= 0 {
		path = path[:tab]
	}
	return pathLanguage(path)
}

func pathLanguage(path string) sourceLanguage {
	path = strings.ToLower(strings.Trim(path, `"`))
	switch {
	case strings.HasSuffix(path, ".go"):
		return sourceLanguageGo
	case strings.HasSuffix(path, ".py"), strings.HasSuffix(path, ".pyi"):
		return sourceLanguagePython
	case strings.HasSuffix(path, ".js"), strings.HasSuffix(path, ".jsx"),
		strings.HasSuffix(path, ".mjs"), strings.HasSuffix(path, ".cjs"),
		strings.HasSuffix(path, ".ts"), strings.HasSuffix(path, ".tsx"),
		strings.HasSuffix(path, ".mts"), strings.HasSuffix(path, ".cts"):
		return sourceLanguageJavaScript
	case strings.HasSuffix(path, ".rs"):
		return sourceLanguageRust
	case strings.HasSuffix(path, ".c"), strings.HasSuffix(path, ".h"),
		strings.HasSuffix(path, ".cc"), strings.HasSuffix(path, ".hh"),
		strings.HasSuffix(path, ".cpp"), strings.HasSuffix(path, ".hpp"),
		strings.HasSuffix(path, ".cxx"), strings.HasSuffix(path, ".hxx"):
		return sourceLanguageC
	case strings.HasSuffix(path, ".java"), strings.HasSuffix(path, ".kt"), strings.HasSuffix(path, ".kts"):
		return sourceLanguageJava
	default:
		return sourceLanguageUnknown
	}
}

func isRawOldFileHeader(lines []sourceLine, index int) bool {
	return index+1 < len(lines) &&
		isFileMarker(lines[index].text, "---") &&
		isFileMarker(lines[index+1].text, "+++")
}

func isFileMarker(line, marker string) bool {
	return strings.HasPrefix(line, marker+" ") || strings.HasPrefix(line, marker+"\t")
}

func isFormatPatchSignature(line string) bool {
	return line == "-- "
}

func isNoNewlineMarker(line string) bool {
	return line == `\ No newline at end of file`
}

// validateRetainedStructure prevents a plan from deleting the metadata that
// identifies retained source. A complete file or hunk may disappear, but a
// surviving line keeps its enclosing diff/file/hunk headers.
func validateRetainedStructure(layout diffLayout, state planState) error {
	var problems []error
	for i, kind := range layout.kinds {
		if kind == diffLineNoNewline && state.represented(i) {
			owner := layout.markerOwner[i]
			if owner < 0 || !state.represented(owner) {
				problems = append(problems, fmt.Errorf("remove: no-newline marker on line %d requires its source line", i+1))
			}
		}

		switch kind {
		case diffLineHeader:
			end := nextLayoutLine(layout, i+1, func(k diffLineKind) bool {
				return k == diffLineHeader || k == diffLineMailSignature
			})
			bodyRetained := anyRetainedMeaningful(layout, state, i+1, end)
			headerRetained := state.represented(i)
			if !bodyRetained && anyRetained(state, i+1, end) {
				problems = append(problems, fmt.Errorf("remove: file beginning on line %d retains only metadata; remove the complete file section", i+1))
			}
			if headerRetained != bodyRetained {
				problems = append(problems, fmt.Errorf("remove: diff header on line %d must be retained exactly when its file body is retained", i+1))
			}
		case diffLineOldFile:
			end := nextLayoutLine(layout, i+2, func(k diffLineKind) bool {
				return k == diffLineHeader || k == diffLineOldFile || k == diffLineMailSignature
			})
			if state.represented(i) != state.represented(i+1) {
				problems = append(problems, fmt.Errorf("remove: ---/+++ headers on lines %d-%d must be removed or retained together", i+1, i+2))
			}
			bodyRetained := anyRetainedMeaningful(layout, state, i+2, end)
			headersRetained := state.represented(i)
			if headersRetained != bodyRetained {
				problems = append(problems, fmt.Errorf("remove: ---/+++ headers on lines %d-%d must be retained exactly when their file body is retained", i+1, i+2))
			}
		case diffLineRenameFrom:
			if i+1 >= len(layout.kinds) || layout.kinds[i+1] != diffLineRenameTo {
				problems = append(problems, fmt.Errorf("rename from metadata on line %d has no matching rename to line", i+1))
			} else if state.represented(i) != state.represented(i+1) {
				problems = append(problems, fmt.Errorf("remove: rename metadata on lines %d-%d must be removed or retained together", i+1, i+2))
			}
		case diffLineCopyFrom:
			if i+1 >= len(layout.kinds) || layout.kinds[i+1] != diffLineCopyTo {
				problems = append(problems, fmt.Errorf("copy from metadata on line %d has no matching copy to line", i+1))
			} else if state.represented(i) != state.represented(i+1) {
				problems = append(problems, fmt.Errorf("remove: copy metadata on lines %d-%d must be removed or retained together", i+1, i+2))
			}
		case diffLineHunkHeader:
			end := nextLayoutLine(layout, i+1, func(k diffLineKind) bool {
				return k == diffLineHeader || k == diffLineOldFile || k == diffLineHunkHeader || k == diffLineMailSignature
			})
			headerRetained := state.represented(i)
			if anyRetainedHunkSource(layout, state, i+1, end) && !headerRetained {
				problems = append(problems, fmt.Errorf("remove: retained context/change lines require hunk header on line %d", i+1))
			}
			changeRetained := anyRetainedHunkChange(layout, state, i+1, end)
			if headerRetained != changeRetained {
				problems = append(problems, fmt.Errorf("remove: hunk header on line %d must be retained exactly when its hunk has a retained change", i+1))
			}
		}
	}
	if len(problems) > 0 {
		return joinEditPlanErrors(problems)
	}
	return nil
}

func anyRetainedMeaningful(layout diffLayout, state planState, start, end int) bool {
	for i := start; i < end; i++ {
		if state.represented(i) && layout.kinds[i] != diffLineNoNewline && layout.kinds[i] != diffLineIndex {
			return true
		}
	}
	return false
}

func anyRetained(state planState, start, end int) bool {
	for i := start; i < end; i++ {
		if state.represented(i) {
			return true
		}
	}
	return false
}

func anyRetainedHunkSource(layout diffLayout, state planState, start, end int) bool {
	for i := start; i < end; i++ {
		if state.represented(i) && isHunkSource(layout.kinds[i]) {
			return true
		}
	}
	return false
}

func anyRetainedHunkChange(layout diffLayout, state planState, start, end int) bool {
	for i := start; i < end; i++ {
		if state.represented(i) && layout.kinds[i] == diffLineHunkChange {
			return true
		}
	}
	return false
}

func nextLayoutLine(layout diffLayout, start int, stop func(diffLineKind) bool) int {
	for i := start; i < len(layout.kinds); i++ {
		if stop(layout.kinds[i]) {
			return i
		}
	}
	return len(layout.kinds)
}
