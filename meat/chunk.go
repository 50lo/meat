// chunk.go splits an oversized unified diff into independently abridgeable
// chunks and merges the per-chunk reading diffs back into one result.
//
// A single agent run re-sends the whole numbered diff every turn, so its size
// is bounded by maxDiffBytes. A larger diff is split at structural boundaries
// — between file sections when possible, between hunks of one oversized file
// otherwise, and between synthesized sub-hunks of one oversized hunk as a
// last resort — into chunks that each fit the single-run budget. Every chunk
// is a well-formed unified diff: a chunk that continues a split file section
// replicates its file-metadata block, and a chunk that begins mid-hunk gets a
// synthesized @@ header whose counts match exactly the lines it carries.
//
// Each chunk is abridged by its own agent run with the same rubric and its
// own 1-based numbering; nothing on the model-visible prompt surface changes.
// The cost is cross-chunk context: exact-move detection runs per chunk, so a
// move whose sides land in different chunks is judged independently on each
// side rather than being enforced symmetric, and a mid-hunk split can leave a
// context-only sub-hunk (which a plan must drop entirely) or start a segment
// inside a Python multiline string (weakening that chunk's Python
// validators). Splitting prefers file boundaries, then hunk boundaries,
// specifically to keep those losses rare.

package meat

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// maxTotalDiffBytes bounds the raw diff accepted for chunked abridging.
// Chunking makes huge diffs feasible, not free: each chunk is a full agent
// run, so cost and wall clock grow linearly with size. Past this cap a
// change should be reviewed in narrower pieces anyway.
const maxTotalDiffBytes = 4 << 20

// diffChunk is one independently abridgeable slice of the original diff.
type diffChunk struct {
	// text is a well-formed unified diff for this slice.
	text string
	// metaPrefix is the file-metadata block (diff --git/index/---/+++ etc.)
	// at the start of the chunk, set for every piece of a split file section.
	// Empty for chunks made of whole sections.
	metaPrefix string
	// sectionID identifies the split file section the chunk belongs to, or -1
	// for a chunk made of whole sections. Pieces of the same section share an
	// ID so the merge step can dedupe their replicated metadata.
	sectionID int
	// continuation marks a chunk that begins mid-file-section: its metaPrefix
	// duplicates metadata already carried by the previous piece, and the merge
	// step drops the duplicate once the file has surfaced in the output.
	continuation bool
}

// numberedLen is the exact size of numberedDiff output for count lines whose
// texts total textLen bytes: each line gains a width-padded number, a '|',
// and a trailing newline.
func numberedLen(textLen, count int) int {
	if count == 0 {
		return 0
	}
	width := len(strconv.Itoa(count))
	return textLen + count*(width+2)
}

// fitsSingleRun reports whether raw and its numbered form both fit within
// budget, i.e. whether one agent run can take the whole diff.
func fitsSingleRun(raw string, budget int) bool {
	if len(raw) > budget {
		return false
	}
	lines := splitSourceLines(raw)
	textLen := 0
	for _, l := range lines {
		textLen += len(l.text)
	}
	return numberedLen(textLen, len(lines)) <= budget
}

// lineSpan is a half-open range [start, end) of physical line indices.
type lineSpan struct {
	start, end int
}

type chunkBuilder struct {
	lines  []sourceLine
	layout diffLayout
	budget int
	// prefixText[i] / prefixRaw[i] are byte totals of lines[:i] without/with
	// line endings, so any span's single-run size is O(1) to check.
	prefixText []int
	prefixRaw  []int
	// hidden marks lines the mandatory import pass removes. Mid-hunk cuts
	// treat a contiguous hidden run as atomic, so a multiline import block is
	// never severed from its opener: each chunk's own import pass must see
	// whole blocks to hide them.
	hidden []bool
	chunks []diffChunk
}

// splitDiffForAbridging cuts raw into chunks that each fit budget both as raw
// text and in numbered form. raw must already have passed
// validateSupportedDiff; every returned chunk passes it too.
func splitDiffForAbridging(raw string, budget int) ([]diffChunk, error) {
	lines := splitSourceLines(raw)
	layout := analyzeDiff(lines)
	if len(layout.problems) > 0 {
		return nil, joinEditPlanErrors(layout.problems)
	}
	b := &chunkBuilder{
		lines:      lines,
		layout:     layout,
		budget:     budget,
		prefixText: make([]int, len(lines)+1),
		prefixRaw:  make([]int, len(lines)+1),
		hidden:     mandatoryRemovalMask(len(lines), mandatoryImportRemovalPlan(lines, layout)),
	}
	for i, l := range lines {
		b.prefixText[i+1] = b.prefixText[i] + len(l.text)
		b.prefixRaw[i+1] = b.prefixRaw[i] + len(l.text) + len(l.eol)
	}

	sections := b.sections()
	if len(sections) == 0 {
		return nil, fmt.Errorf("diff is %dKB with no file sections to split into abridgeable chunks — try a narrower range", len(raw)>>10)
	}

	open := -1 // start line of the accumulating whole-section chunk
	openEnd := 0
	flush := func() {
		if open >= 0 {
			b.chunks = append(b.chunks, diffChunk{text: b.rangeText(open, openEnd), sectionID: -1})
			open = -1
		}
	}
	for id, s := range sections {
		if open >= 0 && b.spanFits(open, s.end) {
			openEnd = s.end
			continue
		}
		flush()
		if b.spanFits(s.start, s.end) {
			open, openEnd = s.start, s.end
			continue
		}
		if err := b.splitSection(id, s); err != nil {
			return nil, err
		}
	}
	flush()
	return b.chunks, nil
}

// sections partitions the diff into per-file spans. Any preamble before the
// first file header (e.g. a git show commit message) joins the first section.
func (b *chunkBuilder) sections() []lineSpan {
	var spans []lineSpan
	for i := range b.lines {
		id := b.layout.fileID[i]
		if id < 0 {
			continue
		}
		if len(spans) == 0 {
			spans = append(spans, lineSpan{start: 0, end: len(b.lines)})
		} else if b.layout.fileID[i-1] != id {
			spans[len(spans)-1].end = i
			spans = append(spans, lineSpan{start: i, end: len(b.lines)})
		}
	}
	return spans
}

func (b *chunkBuilder) rangeText(start, end int) string {
	var sb strings.Builder
	sb.Grow(b.prefixRaw[end] - b.prefixRaw[start])
	for i := start; i < end; i++ {
		sb.WriteString(b.lines[i].text)
		sb.WriteString(b.lines[i].eol)
	}
	return sb.String()
}

func (b *chunkBuilder) spanSizes(start, end int) (textLen, rawLen, count int) {
	return b.prefixText[end] - b.prefixText[start], b.prefixRaw[end] - b.prefixRaw[start], end - start
}

func (b *chunkBuilder) fits(textLen, rawLen, count int) bool {
	return rawLen <= b.budget && numberedLen(textLen, count) <= b.budget
}

func (b *chunkBuilder) spanFits(start, end int) bool {
	return b.fits(b.spanSizes(start, end))
}

// splitSection cuts one oversized file section into pieces at hunk
// boundaries, replicating the file-metadata block on every piece; a hunk
// that is itself oversized is split further by splitHunk. Preamble lines
// before the file's metadata (e.g. a git show commit message) travel with the
// first piece only and are never replicated.
func (b *chunkBuilder) splitSection(sectionID int, s lineSpan) error {
	firstHunk := s.end
	for i := s.start; i < s.end; i++ {
		if b.layout.kinds[i] == diffLineHunkHeader {
			firstHunk = i
			break
		}
	}
	if firstHunk == s.end {
		return fmt.Errorf("file section at line %d is %dKB with no hunks to split — try a narrower diff (per-file with `git diff -- <path> | meat`)",
			s.start+1, (b.prefixRaw[s.end]-b.prefixRaw[s.start])>>10)
	}
	metaStart := firstHunk
	for i := s.start; i < firstHunk; i++ {
		if b.layout.fileID[i] >= 0 {
			metaStart = i
			break
		}
	}
	preamble := lineSpan{start: s.start, end: metaStart}
	preambleText := b.rangeText(preamble.start, preamble.end)
	meta := lineSpan{start: metaStart, end: firstHunk}
	metaText := b.rangeText(meta.start, meta.end)

	var hunks []lineSpan
	for i := firstHunk; i < s.end; {
		j := i + 1
		for j < s.end && b.layout.kinds[j] != diffLineHunkHeader {
			j++
		}
		hunks = append(hunks, lineSpan{start: i, end: j})
		i = j
	}

	piece := 0
	emit := func(body string) {
		prefix := metaText
		if piece == 0 {
			prefix = preambleText + metaText
		}
		b.chunks = append(b.chunks, diffChunk{
			text:         prefix + body,
			metaPrefix:   metaText,
			sectionID:    sectionID,
			continuation: piece > 0,
		})
		piece++
	}
	// prefixSizes is the byte/line cost every piece pays before its body: the
	// replicated metadata, plus the preamble on the first piece.
	prefixSizes := func() (textLen, rawLen, count int) {
		textLen, rawLen, count = b.spanSizes(meta.start, meta.end)
		if piece == 0 {
			pt, pr, pc := b.spanSizes(preamble.start, preamble.end)
			textLen += pt
			rawLen += pr
			count += pc
		}
		return
	}
	runFits := func(start, end int) bool {
		pt, pr, pc := prefixSizes()
		t, r, c := b.spanSizes(start, end)
		return b.fits(pt+t, pr+r, pc+c)
	}

	open := -1 // start line of the accumulating hunk run
	openEnd := 0
	flush := func() {
		if open >= 0 {
			emit(b.rangeText(open, openEnd))
			open = -1
		}
	}
	for _, h := range hunks {
		if open >= 0 && runFits(open, h.end) {
			openEnd = h.end
			continue
		}
		flush()
		if runFits(h.start, h.end) {
			open, openEnd = h.start, h.end
			continue
		}
		if err := b.splitHunk(h, prefixSizes, emit); err != nil {
			return err
		}
	}
	flush()
	return nil
}

// splitHunk cuts one oversized hunk into segments, each emitted as its own
// piece body: a synthesized @@ header plus the segment's hunk lines. Header
// starts continue the original ranges by the rows consumed before the segment
// (a zero-count side names the line before the gap, per unified-diff
// convention) and counts match the emitted body exactly, so every piece
// passes hunk-count validation. A no-newline marker always travels with the
// source line that owns it.
//
// Segments pre-apply the whole-diff mandatory import mask: rows the import
// pass hides are dropped from the emitted text (with header counts and start
// offsets accounting for them). A segment's own compiler cannot re-derive
// those removals — a cut can sever an import block or embedded-string opener
// from the rows that identify it — and dropped rows could never appear in a
// result anyway. For the same reason the original hunk heading is carried
// only by a segment that starts at the true top of the hunk body: replicated
// onto later segments, a heading like a function or import opener would
// describe context the segment does not actually start inside. Segments left
// with no changed rows (context only, or import-only after the drop) are not
// emitted; the whole-diff compiler would never retain them against an empty
// plan either.
func (b *chunkBuilder) splitHunk(h lineSpan, prefixSizes func() (textLen, rawLen, count int), emit func(string)) error {
	oldStart, oldZero, newStart, newZero, heading := parseHunkHeaderForSplit(b.lines[h.start].text)
	headerEOL := b.lines[h.start].eol
	if headerEOL == "" {
		headerEOL = "\n"
	}
	bodyStart, bodyEnd := h.start+1, h.end

	// unitEnd returns the end of the atomic unit starting at line i: the line
	// plus any no-newline markers bound to it.
	unitEnd := func(i int) int {
		j := i + 1
		for j < bodyEnd && b.layout.kinds[j] == diffLineNoNewline {
			j++
		}
		return j
	}
	unitRows := func(start, end int) (uo, un int) {
		for i := start; i < end; i++ {
			if !isHunkSource(b.layout.kinds[i]) || len(b.lines[i].text) == 0 {
				continue
			}
			switch b.lines[i].text[0] {
			case ' ':
				uo++
				un++
			case '-':
				uo++
			case '+':
				un++
			}
		}
		return uo, un
	}

	oldOff, newOff := 0, 0
	atBodyStart := true
	i := bodyStart
	for i < bodyEnd {
		if b.hidden[i] {
			next := unitEnd(i)
			uo, un := unitRows(i, next)
			oldOff += uo
			newOff += un
			i = next
			atBodyStart = false
			continue
		}

		segHeading := ""
		if atBodyStart {
			segHeading = heading
		}
		segOldStart, segNewStart := oldStart+oldOff, newStart+newOff
		segOld, segNew := 0, 0
		segTextLen, segRawLen, segCount := 0, 0, 0
		var spans []lineSpan
		hasChange := false
		for i < bodyEnd {
			if b.hidden[i] {
				// Dropped inside the segment: advances original coordinates
				// but contributes no text, rows, or budget.
				next := unitEnd(i)
				uo, un := unitRows(i, next)
				oldOff += uo
				newOff += un
				i = next
				continue
			}
			next := unitEnd(i)
			uo, un := unitRows(i, next)
			header := synthHunkHeader(segOldStart, segOld+uo, oldZero, segNewStart, segNew+un, newZero, segHeading)
			pt, pr, pc := prefixSizes()
			t, r, c := b.spanSizes(i, next)
			if !b.fits(pt+len(header)+segTextLen+t, pr+len(header)+len(headerEOL)+segRawLen+r, pc+1+segCount+c) {
				break
			}
			if b.layout.kinds[i] == diffLineHunkChange {
				hasChange = true
			}
			segOld += uo
			segNew += un
			segTextLen += t
			segRawLen += r
			segCount += c
			oldOff += uo
			newOff += un
			if n := len(spans); n > 0 && spans[n-1].end == i {
				spans[n-1].end = next
			} else {
				spans = append(spans, lineSpan{start: i, end: next})
			}
			i = next
		}
		atBodyStart = false
		if segCount == 0 {
			return fmt.Errorf("cannot split the diff near line %d into a chunk under the size limit — try a narrower diff (per-file with `git diff -- <path> | meat`)", i+1)
		}
		if !hasChange {
			continue
		}
		var sb strings.Builder
		sb.WriteString(synthHunkHeader(segOldStart, segOld, oldZero, segNewStart, segNew, newZero, segHeading))
		sb.WriteString(headerEOL)
		for _, sp := range spans {
			sb.WriteString(b.rangeText(sp.start, sp.end))
		}
		emit(sb.String())
	}
	return nil
}

// synthHunkHeader renders a segment's @@ header from its side starts and
// emitted-row counts. When the segment has no rows on a side whose original
// range was nonempty, unified-diff convention names the line before the gap
// (an originally empty side already does).
func synthHunkHeader(oldStart, oldCount int, oldZero bool, newStart, newCount int, newZero bool, heading string) string {
	o := gapAdjustedStart(oldStart, oldCount, oldZero)
	n := gapAdjustedStart(newStart, newCount, newZero)
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@%s", o, oldCount, n, newCount, heading)
}

func gapAdjustedStart(start, count int, originallyZero bool) int {
	if count == 0 && !originallyZero {
		start--
		if start < 0 {
			start = 0
		}
	}
	return start
}

// parseHunkHeaderForSplit extracts the range starts, whether each side's
// original count is zero, and the trailing section heading from an @@ header.
// An unparseable header (e.g. a bare @@) yields starts of 1 and no heading;
// only reachable for hunks so large they must be split, where approximate
// starts still beat refusing the diff.
func parseHunkHeaderForSplit(text string) (oldStart int, oldZero bool, newStart int, newZero bool, heading string) {
	oldStart, newStart = 1, 1
	rest, ok := strings.CutPrefix(text, "@@ ")
	if !ok {
		return
	}
	closer := strings.Index(rest, " @@")
	if closer < 0 {
		return
	}
	heading = rest[closer+3:]
	fields := strings.Fields(rest[:closer])
	if len(fields) != 2 {
		return
	}
	if v, zero, ok := parseHunkStart(fields[0], '-'); ok {
		oldStart, oldZero = v, zero
	}
	if v, zero, ok := parseHunkStart(fields[1], '+'); ok {
		newStart, newZero = v, zero
	}
	return
}

func parseHunkStart(field string, sign byte) (start int, zeroCount, ok bool) {
	if len(field) < 2 || field[0] != sign {
		return 0, false, false
	}
	s := field[1:]
	if comma := strings.IndexByte(s, ','); comma >= 0 {
		zeroCount = s[comma+1:] == "0"
		s = s[:comma]
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return 0, false, false
	}
	return v, zeroCount, true
}

// chunkError names the chunk whose agent run failed while preserving the
// wrapped error chain, so errors.Is/As (cancellation, deadline, typed model
// errors) behave the same for chunked and single-run diffs.
type chunkError struct {
	label string
	err   error
}

func (e *chunkError) Error() string {
	// abridgeOne errors already carry the "meat:" prefix; splice the chunk
	// label in rather than stacking prefixes.
	return "meat: " + e.label + ": " + strings.TrimPrefix(e.err.Error(), "meat: ")
}

func (e *chunkError) Unwrap() error { return e.err }

// abridgeChunked splits an oversized diff, runs the normal agent loop on each
// chunk, and merges the results: reading diffs are concatenated in original
// order (dropping a continuation piece's replicated metadata once its file
// has surfaced), summaries are deduplicated and joined, token usage is
// summed. The per-run wall-clock budget applies to each chunk individually.
func abridgeChunked(ctx context.Context, model Model, req Request) (*Result, error) {
	chunks, err := splitDiffForAbridging(req.UnifiedDiff, singleRunDiffBytes)
	if err != nil {
		return nil, fmt.Errorf("meat: %w", err)
	}
	progress := req.Progress
	if progress == nil {
		progress = func(string) {}
	}
	progress(fmt.Sprintf("large diff: abridging %d chunks", len(chunks)))

	merged := &Result{}
	var parts []string
	var summaries []string
	seenSummary := make(map[string]bool)
	emittedMeta := make(map[int]bool)
	for i, chunk := range chunks {
		label := fmt.Sprintf("chunk %d/%d", i+1, len(chunks))
		sub := req
		sub.UnifiedDiff = chunk.text
		sub.Progress = func(msg string) { progress(label + ": " + msg) }
		res, err := abridgeOne(ctx, model, sub)
		if err != nil {
			return nil, &chunkError{label: label, err: err}
		}
		merged.InputTokens += res.InputTokens
		merged.OutputTokens += res.OutputTokens
		if strings.TrimSpace(res.SmartDiff) != "" {
			piece := res.SmartDiff
			if chunk.sectionID >= 0 {
				// Dedupe a split file's replicated metadata, but only once the
				// file header has actually surfaced in the output: a piece may
				// legally elide its whole file section while retaining
				// non-file preamble, and stripping the next piece's headers
				// then would orphan its hunks.
				if emittedMeta[chunk.sectionID] {
					piece = stripReplicatedMeta(piece, chunk.metaPrefix)
				} else if pieceContainsLine(piece, firstLineText(chunk.metaPrefix)) {
					emittedMeta[chunk.sectionID] = true
				}
			}
			if piece != "" {
				// Pieces join on line boundaries, but the last piece may keep
				// a missing final newline from the original.
				if len(parts) > 0 && !strings.HasSuffix(parts[len(parts)-1], "\n") {
					parts[len(parts)-1] += "\n"
				}
				parts = append(parts, piece)
			}
		}
		if s := strings.TrimSpace(res.Summary); s != "" && !seenSummary[s] {
			seenSummary[s] = true
			summaries = append(summaries, s)
		}
	}
	merged.SmartDiff = strings.Join(parts, "")
	merged.Summary = strings.Join(summaries, " ")
	return merged, nil
}

// firstLineText returns the text of s's first physical line.
func firstLineText(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSuffix(s, "\r")
}

// pieceContainsLine reports whether any physical line of piece equals text.
func pieceContainsLine(piece, text string) bool {
	for _, l := range splitSourceLines(piece) {
		if l.text == text {
			return true
		}
	}
	return false
}

// stripReplicatedMeta drops the leading lines of a continuation piece's
// reading diff that replicate its file-metadata block. Matching is in-order
// and stops at the first non-metadata line (the piece's first @@ header, which
// can never appear in the block), so retained hunk content is never touched.
func stripReplicatedMeta(smart, metaPrefix string) string {
	metaLines := splitSourceLines(metaPrefix)
	smartLines := splitSourceLines(smart)
	drop, j := 0, 0
	for _, l := range smartLines {
		k := j
		for k < len(metaLines) && metaLines[k].text != l.text {
			k++
		}
		if k == len(metaLines) {
			break
		}
		j = k + 1
		drop++
	}
	var sb strings.Builder
	for _, l := range smartLines[drop:] {
		sb.WriteString(l.text)
		sb.WriteString(l.eol)
	}
	return sb.String()
}
