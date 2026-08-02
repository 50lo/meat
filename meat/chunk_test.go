package meat

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// setSingleRunBudget shrinks the per-run diff budget so chunking tests can
// exercise real splits with small fixtures. It returns a restore func.
func setSingleRunBudget(t *testing.T, budget int) func() {
	t.Helper()
	old := singleRunDiffBytes
	singleRunDiffBytes = budget
	return func() { singleRunDiffBytes = old }
}

// fileSection builds one file section with a single hunk of n added rows.
func fileSection(name string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -0,0 +1,%d @@\n", name, name, name, name, n)
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "+%s row %d\n", name, i)
	}
	return b.String()
}

// requireValidChunks asserts that every chunk fits the budget as raw and
// numbered text and is independently a supported, well-formed diff.
func requireValidChunks(t *testing.T, chunks []diffChunk, budget int) {
	t.Helper()
	for i, c := range chunks {
		if len(c.text) > budget {
			t.Errorf("chunk %d raw size %d exceeds budget %d", i, len(c.text), budget)
		}
		if n := numberedDiff(c.text); len(n) > budget {
			t.Errorf("chunk %d numbered size %d exceeds budget %d", i, len(n), budget)
		}
		if err := validateSupportedDiff(c.text); err != nil {
			t.Errorf("chunk %d is not independently valid: %v\n%s", i, err, c.text)
		}
	}
}

// reassembleBodies strips synthesized/replicated structure and concatenates
// each chunk's hunk source rows, to prove no original content is lost or
// reordered by splitting.
func chunkBodyRows(text string) []string {
	lines := splitSourceLines(text)
	layout := analyzeDiff(lines)
	var rows []string
	for i, l := range lines {
		if isHunkSource(layout.kinds[i]) || layout.kinds[i] == diffLineNoNewline {
			rows = append(rows, l.text)
		}
	}
	return rows
}

func TestSplitDiff_PacksWholeFileSections(t *testing.T) {
	diff := fileSection("a", 5) + fileSection("b", 5) + fileSection("c", 5)
	one := fileSection("a", 5)
	budget := len(numberedDiff(one + one))
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	requireValidChunks(t, chunks, budget)
	if len(chunks) != 2 {
		t.Fatalf("chunks = %d, want 2 (two sections packed, then one)", len(chunks))
	}
	if !strings.Contains(chunks[0].text, "diff --git a/a") || !strings.Contains(chunks[0].text, "diff --git a/b") {
		t.Errorf("first chunk should pack sections a and b:\n%s", chunks[0].text)
	}
	if !strings.Contains(chunks[1].text, "diff --git a/c") || chunks[1].continuation {
		t.Errorf("second chunk should be whole section c, not a continuation:\n%s", chunks[1].text)
	}
	if joined := chunks[0].text + chunks[1].text; joined != diff {
		t.Errorf("whole-section chunks should reassemble the original diff exactly")
	}
}

func TestSplitDiff_SplitsOversizedFileAtHunks(t *testing.T) {
	// One file, several hunks, each hunk well under budget but the file over.
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	for h := 0; h < 6; h++ {
		fmt.Fprintf(&b, "@@ -%d,2 +%d,3 @@ func f%d()\n context %d\n+added %d\n context tail %d\n", h*10+1, h*10+1, h, h, h, h)
	}
	diff := b.String()
	budget := len(numberedDiff(diff))/2 + 40
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	requireValidChunks(t, chunks, budget)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want a real split", len(chunks))
	}
	var rows []string
	for i, c := range chunks {
		if !strings.HasPrefix(c.text, "diff --git a/big.go") {
			t.Errorf("chunk %d lost its replicated file metadata:\n%s", i, c.text)
		}
		if (i > 0) != c.continuation {
			t.Errorf("chunk %d continuation = %v, want %v", i, c.continuation, i > 0)
		}
		if c.sectionID < 0 {
			t.Errorf("chunk %d of a split section should carry its section ID", i)
		}
		// Hunk-boundary splits keep original @@ headers (with their headings).
		if !strings.Contains(c.text, " @@ func f") {
			t.Errorf("chunk %d lost hunk headings:\n%s", i, c.text)
		}
		rows = append(rows, chunkBodyRows(c.text)...)
	}
	if want := chunkBodyRows(diff); strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Errorf("split lost or reordered hunk rows:\ngot:\n%s\nwant:\n%s", strings.Join(rows, "\n"), strings.Join(want, "\n"))
	}
}

func TestSplitDiff_SplitsOversizedHunkWithConsistentCounts(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n@@ -100,30 +200,45 @@ func big()\n")
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&b, " context row %02d\n", i)
		if i%2 == 0 {
			fmt.Fprintf(&b, "+added row %02d\n", i)
		}
	}
	diff := b.String()
	budget := len(numberedDiff(diff))/3 + 60
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	// requireValidChunks runs full hunk-count validation on every synthesized
	// header, which is the heart of this test.
	requireValidChunks(t, chunks, budget)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want a real split", len(chunks))
	}
	var rows []string
	for _, c := range chunks {
		rows = append(rows, chunkBodyRows(c.text)...)
	}
	if want := chunkBodyRows(diff); strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Errorf("hunk split lost or reordered rows")
	}
	// Synthesized headers continue the original starts and keep the heading.
	if !strings.Contains(chunks[0].text, "@@ -100,") || !strings.Contains(chunks[0].text, "+200,") {
		t.Errorf("first segment header should keep original starts:\n%s", chunks[0].text)
	}
	for i, c := range chunks {
		if !strings.Contains(c.text, " @@ func big()") {
			t.Errorf("chunk %d lost the hunk heading:\n%s", i, c.text)
		}
	}
}

func TestSplitDiff_KeepsNoNewlineMarkerWithItsLine(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,6 +1,6 @@\n")
	for i := 0; i < 5; i++ {
		fmt.Fprintf(&b, " ctx %d\n", i)
	}
	b.WriteString("-old tail\n+new tail\n\\ No newline at end of file\n")
	diff := b.String()
	// Budget forces a split right around the trailing marker.
	meta := "diff --git a/a b/a\n--- a/a\n+++ b/a\n@@ -1,6 +1,6 @@\n"
	budget := len(numberedDiff(meta)) + 80
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	requireValidChunks(t, chunks, budget)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want a real split around the marker", len(chunks))
	}
	for i, c := range chunks {
		if strings.Contains(c.text, "No newline") && !strings.Contains(c.text, "+new tail\n\\ No newline") {
			t.Errorf("chunk %d separated the no-newline marker from its source line:\n%s", i, c.text)
		}
	}
}

func TestSplitDiff_UnsplittableSectionFails(t *testing.T) {
	diff := "diff --git a/x b/x\nBinary files a/x and b/x differ\n" + strings.Repeat("junk\n", 100)
	_, err := splitDiffForAbridging(diff, 80)
	if err == nil || !strings.Contains(err.Error(), "narrower") {
		t.Fatalf("err = %v, want unsplittable-section advice", err)
	}
}

// TestSplitDiff_SynthesizedHeadersUseGapConvention pins the unified-diff
// coordinate convention for synthesized segment headers: a side with no rows
// in the segment names the line BEFORE the gap, and a side with rows names
// its first row, continuing the original starts by rows already consumed.
func TestSplitDiff_SynthesizedHeadersUseGapConvention(t *testing.T) {
	meta := "diff --git a/a b/a\n--- a/a\n+++ b/a\n"
	diff := meta + "@@ -10,2 +20,2 @@\n-old10\n-old11\n+new20\n+new21\n"
	budget := len(numberedDiff(meta + "@@ -10,1 +19,0 @@\n-old10\n"))
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	requireValidChunks(t, chunks, budget)
	want := []string{
		"@@ -10,1 +19,0 @@\n-old10\n",
		"@@ -11,1 +19,0 @@\n-old11\n",
		"@@ -11,0 +20,1 @@\n+new20\n",
		"@@ -11,0 +21,1 @@\n+new21\n",
	}
	if len(chunks) != len(want) {
		t.Fatalf("chunks = %d, want %d single-row segments", len(chunks), len(want))
	}
	for i, c := range chunks {
		if c.text != meta+want[i] {
			t.Errorf("chunk %d = %q, want %q", i, c.text, meta+want[i])
		}
	}
}

// TestSplitDiff_OriginallyEmptySideKeepsItsStart: a side whose ORIGINAL range
// is zero-count (pure insertion/deletion hunks) already names the line before
// the gap; splitting must not decrement it again.
func TestSplitDiff_OriginallyEmptySideKeepsItsStart(t *testing.T) {
	meta := "diff --git a/a b/a\n--- a/a\n+++ b/a\n"
	diff := meta + "@@ -10,0 +11,4 @@\n+r0\n+r1\n+r2\n+r3\n"
	budget := len(numberedDiff(meta + "@@ -10,0 +11,2 @@\n+r0\n+r1\n"))
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	requireValidChunks(t, chunks, budget)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want a real split", len(chunks))
	}
	wantNew := 11
	for i, c := range chunks {
		if !strings.Contains(c.text, "@@ -10,0 ") {
			t.Errorf("chunk %d shifted the originally empty old side, want -10,0:\n%s", i, c.text)
		}
		if !strings.Contains(c.text, fmt.Sprintf("+%d,", wantNew)) {
			t.Errorf("chunk %d new start: want +%d:\n%s", i, wantNew, c.text)
		}
		wantNew += strings.Count(c.text, "\n+r")
	}
}

// TestSplitDiff_ZeroStartClampsAtZero: splitting a new-file hunk (old side
// @@ -0,0) must not synthesize a negative old start.
func TestSplitDiff_ZeroStartClampsAtZero(t *testing.T) {
	meta := "diff --git a/a b/a\n--- /dev/null\n+++ b/a\n"
	diff := meta + "@@ -0,0 +1,4 @@\n+r0\n+r1\n+r2\n+r3\n"
	budget := len(numberedDiff(meta + "@@ -0,0 +1,2 @@\n+r0\n+r1\n"))
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	requireValidChunks(t, chunks, budget)
	for i, c := range chunks {
		if !strings.Contains(c.text, "@@ -0,0 ") {
			t.Errorf("chunk %d old side = should stay -0,0:\n%s", i, c.text)
		}
	}
}

// TestSplitDiff_KeepsMultilineImportBlockAtomic: a mid-hunk cut must never
// land inside a multiline import block. Each chunk's compiler runs its own
// mandatory import pass, which recognizes a block only from its opener; a
// severed tail would surface import members in the merged reading diff.
func TestSplitDiff_KeepsMultilineImportBlockAtomic(t *testing.T) {
	meta := "diff --git a/a.go b/a.go\n--- /dev/null\n+++ b/a.go\n"
	// Rows before the import block position the greedy cut inside the block;
	// atomicity must push the whole block into the next chunk instead.
	const before, members, after = 10, 8, 10
	var body strings.Builder
	fmt.Fprintf(&body, "@@ -0,0 +1,%d @@\n+package a\n+\n", 5+before+members+after)
	for i := 0; i < before; i++ {
		fmt.Fprintf(&body, "+var a%d = %d\n", i, i)
	}
	body.WriteString("+import (\n")
	for i := 0; i < members; i++ {
		fmt.Fprintf(&body, "+\t%q\n", fmt.Sprintf("pkg%d", i))
	}
	body.WriteString("+)\n+func F() {}\n")
	for i := 0; i < after; i++ {
		fmt.Fprintf(&body, "+var b%d = %d\n", i, i)
	}
	diff := meta + body.String()
	// Budget sized so a per-line greedy cut would land mid-block: everything
	// up to the block plus half its members.
	prefix := strings.SplitAfter(diff, `"pkg3"`+"\n")[0]
	budget := len(numberedDiff(prefix))
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	requireValidChunks(t, chunks, budget)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want a real split", len(chunks))
	}
	sawBlock := false
	for i, c := range chunks {
		if !strings.Contains(c.text, `"pkg`) {
			continue
		}
		sawBlock = true
		if !strings.Contains(c.text, "import (") || !strings.Contains(c.text, `"pkg7"`) {
			t.Errorf("chunk %d severed the multiline import block:\n%s", i, c.text)
		}
	}
	if !sawBlock {
		t.Fatal("fixture lost its import block")
	}

	// End to end: the merged reading diff must contain no import scaffolding.
	defer setSingleRunBudget(t, budget)()
	m := &scriptedModel{turns: []*Response{assistant(toolUse("s", "submit", submission{
		Remove: []lineRange{}, Replace: []lineReplacement{}, Fold: []lineFold{}, Summary: "Adds file.",
	}))}}
	res, err := Abridge(context.Background(), m, Request{UnifiedDiff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(res.SmartDiff, `"pkg`) || strings.Contains(res.SmartDiff, "+)") || strings.Contains(res.SmartDiff, "import (") {
		t.Errorf("import scaffolding leaked into the merged reading diff:\n%s", res.SmartDiff)
	}
	if !strings.Contains(res.SmartDiff, "+func F() {}") {
		t.Errorf("behavioral row lost from merged reading diff:\n%s", res.SmartDiff)
	}
}

// TestSplitDiff_PreservesCRLF: synthesized segment headers adopt the original
// hunk header's line ending, so a CRLF diff stays CRLF throughout.
func TestSplitDiff_PreservesCRLF(t *testing.T) {
	crlf := func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }
	meta := crlf("diff --git a/a b/a\n--- a/a\n+++ b/a\n")
	var body strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&body, "+row %d\r\n", i)
	}
	diff := meta + crlf("@@ -0,0 +1,12 @@\n") + body.String()
	budget := len(numberedDiff(diff))/2 + 60
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	requireValidChunks(t, chunks, budget)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want a real split", len(chunks))
	}
	for i, c := range chunks {
		if strings.Contains(strings.ReplaceAll(c.text, "\r\n", ""), "\n") {
			t.Errorf("chunk %d contains a bare LF line (synthesized header?):\n%q", i, c.text)
		}
	}
}

func TestSplitDiff_OverlongSingleLineFails(t *testing.T) {
	diff := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -0,0 +1 @@\n+" + strings.Repeat("x", 500) + "\n"
	_, err := splitDiffForAbridging(diff, 200)
	if err == nil || !strings.Contains(err.Error(), "narrower") {
		t.Fatalf("err = %v, want unsplittable advice", err)
	}
}

// TestAbridge_ChunkedEndToEnd drives a whole chunked run through the public
// Abridge entry point with a scripted model: an oversized two-file diff is
// split, each chunk gets its own agent run with its own 1-based numbering,
// and the merged result concatenates the per-chunk reading diffs, joins the
// distinct summaries, and sums token usage.
func TestAbridge_ChunkedEndToEnd(t *testing.T) {
	defer setSingleRunBudget(t, 400)()

	secA := fileSection("alpha.go", 10)
	secB := fileSection("beta.go", 10)
	diff := secA + secB
	if fitsSingleRun(diff, 400) {
		t.Fatal("fixture should exceed the shrunken single-run budget")
	}
	if !fitsSingleRun(secA, 400) || !fitsSingleRun(secB, 400) {
		t.Fatal("each section should fit alone")
	}

	// Each chunk run submits a plan valid for that chunk in ITS OWN 1-based
	// numbering: keep the header block, first and last added rows, and remove
	// the repetitive middle (rows 6-13 of the 14-line chunk).
	plan := func(summary string) submission {
		return submission{
			Remove:  []lineRange{{StartLine: 6, EndLine: 13}},
			Replace: []lineReplacement{},
			Fold:    []lineFold{},
			Summary: summary,
		}
	}
	m := &scriptedModel{turns: []*Response{
		assistant(toolUse("s1", "submit", plan("Adds alpha rows."))),
		assistant(toolUse("s2", "submit", plan("Adds beta rows."))),
	}}
	var progress []string
	res, err := Abridge(context.Background(), m, Request{
		UnifiedDiff: diff,
		Progress:    func(s string) { progress = append(progress, s) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.seen != 2 {
		t.Fatalf("model calls = %d, want 2 (one per chunk)", m.seen)
	}

	// Each run saw only its own chunk, renumbered from 1.
	for i, msgs := range m.seenMessages {
		prompt := msgs[0].Content[0].Text
		if !strings.Contains(prompt, " 1|diff --git") {
			t.Errorf("chunk %d prompt does not restart numbering at 1", i)
		}
	}
	if !strings.Contains(m.seenMessages[0][0].Content[0].Text, "alpha.go") ||
		strings.Contains(m.seenMessages[0][0].Content[0].Text, "beta.go") {
		t.Error("first chunk prompt should contain only alpha.go")
	}
	if !strings.Contains(m.seenMessages[1][0].Content[0].Text, "beta.go") ||
		strings.Contains(m.seenMessages[1][0].Content[0].Text, "alpha.go") {
		t.Error("second chunk prompt should contain only beta.go")
	}

	for _, want := range []string{"diff --git a/alpha.go", "diff --git a/beta.go", "+alpha.go row 0", "+beta.go row 0"} {
		if !strings.Contains(res.SmartDiff, want) {
			t.Errorf("merged smart diff missing %q:\n%s", want, res.SmartDiff)
		}
	}
	if res.Summary != "Adds alpha rows. Adds beta rows." {
		t.Errorf("merged summary = %q", res.Summary)
	}
	if res.InputTokens != 200 || res.OutputTokens != 40 {
		t.Errorf("merged tokens = %d/%d, want summed 200/40", res.InputTokens, res.OutputTokens)
	}
	joined := strings.Join(progress, "\n")
	for _, want := range []string{"abridging 2 chunks", "chunk 1/2: thinking", "chunk 2/2: thinking"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress missing %q:\n%s", want, joined)
		}
	}

	// The merged output still aligns for the local elision manifest.
	if line := ElisionLine(diff, res.SmartDiff); !strings.Contains(line, "in 2/2 files") {
		t.Errorf("ElisionLine = %q, want file counts over the merged diff", line)
	}
}

// TestAbridge_ChunkedMergeDedupesSplitFileMetadata: when one file is split
// into pieces and both pieces retain content, the merged reading diff carries
// the file's metadata block once.
func TestAbridge_ChunkedMergeDedupesSplitFileMetadata(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	for h := 0; h < 4; h++ {
		fmt.Fprintf(&b, "@@ -%d,1 +%d,2 @@ func f%d()\n context %d\n+added %d\n", h*10+1, h*10+1, h, h, h)
	}
	diff := b.String()
	budget := len(numberedDiff(diff))/2 + 60
	defer setSingleRunBudget(t, budget)()
	if fitsSingleRun(diff, budget) {
		t.Fatal("fixture should exceed the shrunken budget")
	}

	// Identity plans: keep everything in both chunks.
	m := &scriptedModel{turns: []*Response{
		assistant(toolUse("s", "submit", submission{
			Remove: []lineRange{}, Replace: []lineReplacement{}, Fold: []lineFold{}, Summary: "Adds rows.",
		})),
	}}
	res, err := Abridge(context.Background(), m, Request{UnifiedDiff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(res.SmartDiff, "diff --git a/big.go"); got != 1 {
		t.Errorf("merged diff repeats file metadata %d times, want 1:\n%s", got, res.SmartDiff)
	}
	if got := strings.Count(res.SmartDiff, "+++ b/big.go"); got != 1 {
		t.Errorf("merged diff repeats +++ header %d times, want 1:\n%s", got, res.SmartDiff)
	}
	for h := 0; h < 4; h++ {
		if !strings.Contains(res.SmartDiff, fmt.Sprintf("+added %d", h)) {
			t.Errorf("merged diff lost hunk %d content:\n%s", h, res.SmartDiff)
		}
	}
	if res.Summary != "Adds rows." {
		t.Errorf("duplicate per-chunk summaries should merge to one: %q", res.Summary)
	}
}

// TestAbridge_ChunkedMergeKeepsMetaWhenFirstPieceEmpty: when the first piece
// of a split file abridges to nothing, the continuation piece's replicated
// metadata is the file's only header and must survive the merge.
func TestAbridge_ChunkedMergeKeepsMetaWhenFirstPieceEmpty(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	for h := 0; h < 4; h++ {
		fmt.Fprintf(&b, "@@ -%d,1 +%d,2 @@ func f%d()\n context %d\n+added %d\n", h*10+1, h*10+1, h, h, h)
	}
	diff := b.String()
	budget := len(numberedDiff(diff))/2 + 60
	defer setSingleRunBudget(t, budget)()

	m := &scriptedModel{turns: []*Response{
		// First chunk: everything is noise; remove all 9 of its lines.
		assistant(toolUse("s1", "submit", submission{
			Remove: []lineRange{{StartLine: 1, EndLine: 9}}, Replace: []lineReplacement{}, Fold: []lineFold{}, Summary: "Nothing meaningful.",
		})),
		// Second chunk: keep everything.
		assistant(toolUse("s2", "submit", submission{
			Remove: []lineRange{}, Replace: []lineReplacement{}, Fold: []lineFold{}, Summary: "Adds rows.",
		})),
	}}
	res, err := Abridge(context.Background(), m, Request{UnifiedDiff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if m.seen != 2 {
		t.Fatalf("model calls = %d, want 2", m.seen)
	}
	if got := strings.Count(res.SmartDiff, "diff --git a/big.go"); got != 1 {
		t.Errorf("merged diff has %d file headers, want the continuation's to survive:\n%s", got, res.SmartDiff)
	}
	if !strings.Contains(res.SmartDiff, "+added 2") || strings.Contains(res.SmartDiff, "+added 0") {
		t.Errorf("merged diff should carry only the second piece's hunks:\n%s", res.SmartDiff)
	}
}

// TestSplitDiff_PreambleTravelsWithFirstPieceOnly: non-file preamble (a git
// show / format-patch message) ahead of a split file section rides the first
// piece and is never replicated onto continuations, and metaPrefix contains
// only the file's own metadata block.
func TestSplitDiff_PreambleTravelsWithFirstPieceOnly(t *testing.T) {
	preamble := "commit 0123456789abcdef\nAuthor: A U Thor <a@example.com>\n\n    Big change.\n\n"
	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	for h := 0; h < 4; h++ {
		fmt.Fprintf(&b, "@@ -%d,1 +%d,2 @@ func f%d()\n context %d\n+added %d\n", h*10+1, h*10+1, h, h, h)
	}
	diff := b.String()
	budget := len(numberedDiff(diff))/2 + 90
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil {
		t.Fatal(err)
	}
	requireValidChunks(t, chunks, budget)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %d, want a real split", len(chunks))
	}
	if !strings.HasPrefix(chunks[0].text, preamble) {
		t.Errorf("first piece lost the preamble:\n%s", chunks[0].text)
	}
	for i, c := range chunks {
		if strings.Contains(c.metaPrefix, "commit ") {
			t.Errorf("chunk %d metaPrefix includes preamble:\n%s", i, c.metaPrefix)
		}
		if i > 0 && strings.Contains(c.text, "commit ") {
			t.Errorf("chunk %d replicated the preamble:\n%s", i, c.text)
		}
		if !strings.Contains(c.text, "diff --git a/big.go") {
			t.Errorf("chunk %d lost file metadata:\n%s", i, c.text)
		}
	}
}

// TestAbridge_ChunkedRetainedPreambleKeepsContinuationHeaders: when the first
// piece elides its whole file section but retains preamble prose, the
// continuation piece's replicated file headers are the file's only headers
// and must not be stripped — otherwise its hunks would be orphaned under the
// commit message.
func TestAbridge_ChunkedRetainedPreambleKeepsContinuationHeaders(t *testing.T) {
	preamble := "commit 0123456789abcdef\nAuthor: A U Thor <a@example.com>\n\n    Big change.\n\n"
	var b strings.Builder
	b.WriteString(preamble)
	b.WriteString("diff --git a/big.go b/big.go\n--- a/big.go\n+++ b/big.go\n")
	for h := 0; h < 4; h++ {
		fmt.Fprintf(&b, "@@ -%d,1 +%d,2 @@ func f%d()\n context %d\n+added %d\n", h*10+1, h*10+1, h, h, h)
	}
	diff := b.String()
	budget := len(numberedDiff(diff))/2 + 90
	defer setSingleRunBudget(t, budget)()
	chunks, err := splitDiffForAbridging(diff, budget)
	if err != nil || len(chunks) != 2 {
		t.Fatalf("chunks = %d (%v), want 2", len(chunks), err)
	}

	// First chunk: remove the whole file section but keep the preamble.
	fileStart := len(splitSourceLines(preamble)) + 1
	firstLen := len(splitSourceLines(chunks[0].text))
	m := &scriptedModel{turns: []*Response{
		assistant(toolUse("s1", "submit", submission{
			Remove: []lineRange{{StartLine: fileStart, EndLine: firstLen}}, Replace: []lineReplacement{}, Fold: []lineFold{}, Summary: "Nothing meaningful.",
		})),
		assistant(toolUse("s2", "submit", submission{
			Remove: []lineRange{}, Replace: []lineReplacement{}, Fold: []lineFold{}, Summary: "Adds rows.",
		})),
	}}
	res, err := Abridge(context.Background(), m, Request{UnifiedDiff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.SmartDiff, "commit 0123456789abcdef") {
		t.Errorf("merged diff lost retained preamble:\n%s", res.SmartDiff)
	}
	if got := strings.Count(res.SmartDiff, "diff --git a/big.go"); got != 1 {
		t.Errorf("merged diff has %d file headers, want the continuation's kept: \n%s", got, res.SmartDiff)
	}
	idx := strings.Index(res.SmartDiff, "@@")
	if hdr := strings.Index(res.SmartDiff, "diff --git"); idx >= 0 && (hdr < 0 || hdr > idx) {
		t.Errorf("merged diff has orphaned hunks before any file header:\n%s", res.SmartDiff)
	}
}

// TestAbridge_ChunkedPreservesMissingFinalNewline: identity plans over a
// chunked diff whose last line has no trailing newline reproduce the input
// byte-for-byte; the merge step must not append one.
func TestAbridge_ChunkedPreservesMissingFinalNewline(t *testing.T) {
	diff := strings.TrimSuffix(fileSection("alpha.go", 10)+fileSection("beta.go", 10), "\n")
	defer setSingleRunBudget(t, 400)()
	if fitsSingleRun(diff, 400) {
		t.Fatal("fixture should exceed the shrunken budget")
	}
	m := &scriptedModel{turns: []*Response{
		assistant(toolUse("s", "submit", submission{
			Remove: []lineRange{}, Replace: []lineReplacement{}, Fold: []lineFold{}, Summary: "Adds rows.",
		})),
	}}
	res, err := Abridge(context.Background(), m, Request{UnifiedDiff: diff})
	if err != nil {
		t.Fatal(err)
	}
	if res.SmartDiff != diff {
		t.Errorf("identity chunked abridgement altered the diff;\ngot:\n%q\nwant:\n%q", res.SmartDiff, diff)
	}
}

// TestAbridge_ChunkedFailurePropagates: a chunk whose run fails aborts the
// whole abridgement with the chunk named, rather than returning a silently
// partial result.
func TestAbridge_ChunkedFailurePropagates(t *testing.T) {
	defer setSingleRunBudget(t, 400)()
	diff := fileSection("alpha.go", 10) + fileSection("beta.go", 10)

	// First chunk submits; second chunk never calls a tool and burns turns.
	m := &scriptedModel{turns: []*Response{
		assistant(toolUse("s1", "submit", submission{
			Remove: []lineRange{}, Replace: []lineReplacement{}, Fold: []lineFold{}, Summary: "Adds alpha rows.",
		})),
		assistant(textBlock("just text")),
	}}
	_, err := Abridge(context.Background(), m, Request{UnifiedDiff: diff, MaxTurns: 2})
	if err == nil || !strings.Contains(err.Error(), "chunk 2/2") {
		t.Fatalf("err = %v, want failure naming chunk 2/2", err)
	}
}

func TestFitsSingleRun(t *testing.T) {
	diff := "diff --git a/a b/a\n@@ -1 +1 @@\n+x\n"
	if !fitsSingleRun(diff, len(numberedDiff(diff))) {
		t.Error("diff should fit a budget equal to its numbered size")
	}
	if fitsSingleRun(diff, len(numberedDiff(diff))-1) {
		t.Error("diff should not fit below its numbered size")
	}
	if fitsSingleRun(strings.Repeat("x", 100), 50) {
		t.Error("raw size over budget should not fit")
	}
}
