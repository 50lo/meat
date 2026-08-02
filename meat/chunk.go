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
	chunks     []diffChunk
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

func (b *chunkBuilder) metaSpanFits(meta lineSpan, start, end int) bool {
	mt, mr, mc := b.spanSizes(meta.start, meta.end)
	t, r, c := b.spanSizes(start, end)
	return b.fits(mt+t, mr+r, mc+c)
}

// splitSection cuts one oversized file section into pieces at hunk
// boundaries, replicating the metadata block on every piece; a hunk that is
// itself oversized is split further by splitHunk.
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
	meta := lineSpan{start: s.start, end: firstHunk}
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
	emit := func(text string) {
		b.chunks = append(b.chunks, diffChunk{
			text:         text,
			metaPrefix:   metaText,
			sectionID:    sectionID,
			continuation: piece > 0,
		})
		piece++
	}

	open := -1 // start line of the accumulating hunk run
	openEnd := 0
	flush := func() {
		if open >= 0 {
			emit(metaText + b.rangeText(open, openEnd))
			open = -1
		}
	}
	for _, h := range hunks {
		if open >= 0 && b.metaSpanFits(meta, open, h.end) {
			openEnd = h.end
			continue
		}
		flush()
		if b.metaSpanFits(meta, h.start, h.end) {
			open, openEnd = h.start, h.end
			continue
		}
		if err := b.splitHunk(meta, metaText, h, emit); err != nil {
			return err
		}
	}
	flush()
	return nil
}

// splitHunk cuts one oversized hunk into segments, each emitted as its own
// piece: replicated metadata plus a synthesized @@ header whose starts are
// offset by the lines consumed before the segment and whose counts match the
// segment body exactly, so every piece passes hunk-count validation. A
// no-newline marker always travels with the source line that owns it.
func (b *chunkBuilder) splitHunk(meta lineSpan, metaText string, h lineSpan, emit func(string)) error {
	metaTextLen, metaRawLen, metaCount := b.spanSizes(meta.start, meta.end)
	oldStart, newStart, heading := parseHunkHeaderForSplit(b.lines[h.start].text)
	bodyStart, bodyEnd := h.start+1, h.end
	oldOff, newOff := 0, 0

	i := bodyStart
	for i < bodyEnd {
		segStart := i
		segOld, segNew := 0, 0
		for i < bodyEnd {
			// A unit is one body line plus any no-newline marker bound to it.
			unitEnd := i + 1
			for unitEnd < bodyEnd && b.layout.kinds[unitEnd] == diffLineNoNewline {
				unitEnd++
			}
			uo, un := 0, 0
			if isHunkSource(b.layout.kinds[i]) && len(b.lines[i].text) > 0 {
				switch b.lines[i].text[0] {
				case ' ':
					uo, un = 1, 1
				case '-':
					uo = 1
				case '+':
					un = 1
				}
			}
			header := synthHunkHeader(oldStart+oldOff, segOld+uo, newStart+newOff, segNew+un, heading)
			t, r, c := b.spanSizes(segStart, unitEnd)
			if !b.fits(metaTextLen+len(header)+t, metaRawLen+len(header)+1+r, metaCount+1+c) {
				break
			}
			segOld += uo
			segNew += un
			i = unitEnd
		}
		if i == segStart {
			return fmt.Errorf("cannot split the diff near line %d into a chunk under the size limit — try a narrower diff (per-file with `git diff -- <path> | meat`)", segStart+1)
		}
		header := synthHunkHeader(oldStart+oldOff, segOld, newStart+newOff, segNew, heading)
		emit(metaText + header + "\n" + b.rangeText(segStart, i))
		oldOff += segOld
		newOff += segNew
	}
	return nil
}

func synthHunkHeader(oldStart, oldCount, newStart, newCount int, heading string) string {
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@%s", oldStart, oldCount, newStart, newCount, heading)
}

// parseHunkHeaderForSplit extracts the range starts and trailing section
// heading from an @@ header. An unparseable header (e.g. a bare @@) yields
// starts of 1 and no heading; only reachable for hunks so large they must be
// split, where approximate starts still beat refusing the diff.
func parseHunkHeaderForSplit(text string) (oldStart, newStart int, heading string) {
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
	if v, ok := parseHunkStart(fields[0], '-'); ok {
		oldStart = v
	}
	if v, ok := parseHunkStart(fields[1], '+'); ok {
		newStart = v
	}
	return
}

func parseHunkStart(field string, sign byte) (int, bool) {
	if len(field) < 2 || field[0] != sign {
		return 0, false
	}
	s := field[1:]
	if comma := strings.IndexByte(s, ','); comma >= 0 {
		s = s[:comma]
	}
	v, err := strconv.Atoi(s)
	if err != nil || v < 0 {
		return 0, false
	}
	return v, true
}

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
			// abridgeOne errors already carry the "meat:" prefix; splice the
			// chunk label in rather than stacking prefixes.
			return nil, fmt.Errorf("meat: %s: %s", label, strings.TrimPrefix(err.Error(), "meat: "))
		}
		merged.InputTokens += res.InputTokens
		merged.OutputTokens += res.OutputTokens
		if strings.TrimSpace(res.SmartDiff) != "" {
			piece := res.SmartDiff
			if chunk.continuation && emittedMeta[chunk.sectionID] {
				piece = stripReplicatedMeta(piece, chunk.metaPrefix)
			}
			if chunk.sectionID >= 0 {
				emittedMeta[chunk.sectionID] = true
			}
			if !strings.HasSuffix(piece, "\n") {
				piece += "\n"
			}
			parts = append(parts, piece)
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
