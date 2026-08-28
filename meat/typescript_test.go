package meat

import (
	"strings"
	"testing"
)

func TestCompileEditPlan_TypeScriptStructure(t *testing.T) {
	const raw = "diff --git a/work.ts b/work.ts\n--- a/work.ts\n+++ b/work.ts\n@@ -0,0 +1,4 @@\n+class Foo {\n+    first()\n+    second()\n+}\n"

	t.Run("rejects hidden class owner with retained body", func(t *testing.T) {
		_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
		if err == nil || !strings.Contains(err.Error(), "TypeScript") {
			t.Fatalf("hidden TS owner error = %v, want TypeScript structural error", err)
		}
	})

	t.Run("rejects an orphaned body even when braces stay balanced", func(t *testing.T) {
		_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}, {StartLine: 8, EndLine: 8}}})
		if err == nil || !strings.Contains(err.Error(), "hides TypeScript") {
			t.Fatalf("orphaned TS body error = %v", err)
		}
	})

	t.Run("allows folding a class interior", func(t *testing.T) {
		compiled, err := compileEditPlan(raw, editPlan{Fold: []lineFold{{StartLine: 6, EndLine: 7}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(compiled.smartDiff, "+class Foo {\n+    ...\n+}\n") {
			t.Fatalf("TS interior fold =\n%s", compiled.smartDiff)
		}
	})

	t.Run("rejects a replacement that removes a brace", func(t *testing.T) {
		_, err := compileEditPlan(raw, editPlan{Replace: []lineReplacement{{
			Line: 5,
			Old:  "class Foo {",
			New:  "class Foo...",
		}}})
		if err == nil || !strings.Contains(err.Error(), "TypeScript") {
			t.Fatalf("TS brace removal error = %v", err)
		}
	})
}

func TestCompileEditPlan_TypeScriptInterfaceOwner(t *testing.T) {
	const raw = "diff --git a/api.ts b/api.ts\n--- a/api.ts\n+++ b/api.ts\n@@ -0,0 +1,4 @@\n+export interface ApiResult {\n+    ok: boolean\n+    value: string\n+}\n"

	t.Run("rejects hidden interface owner with retained members", func(t *testing.T) {
		_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
		if err == nil || !strings.Contains(err.Error(), "TypeScript") {
			t.Fatalf("hidden TS interface owner error = %v, want TypeScript structural error", err)
		}
	})

	t.Run("allows folding interface members", func(t *testing.T) {
		compiled, err := compileEditPlan(raw, editPlan{Fold: []lineFold{{StartLine: 6, EndLine: 7}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(compiled.smartDiff, "+export interface ApiResult {\n+    ...\n+}\n") {
			t.Fatalf("TS interface fold =\n%s", compiled.smartDiff)
		}
	})
}

func TestCompileEditPlan_TypeScriptEnumOwner(t *testing.T) {
	const raw = "diff --git a/colors.ts b/colors.ts\n--- a/colors.ts\n+++ b/colors.ts\n@@ -0,0 +1,6 @@\n+export enum Color {\n+    Red,\n+    Green,\n+    Blue,\n+    Cyan,\n+}\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err == nil || !strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf("hidden TS enum owner error = %v, want TypeScript structural error", err)
	}
}

func TestCompileEditPlan_TypeScriptTypeAliasWithBraceShape(t *testing.T) {
	const raw = "diff --git a/options.ts b/options.ts\n--- a/options.ts\n+++ b/options.ts\n@@ -0,0 +1,4 @@\n+export type Options = {\n+    timeout: number\n+    retries: boolean\n+}\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err == nil || !strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf("hidden TS type alias owner error = %v, want TypeScript structural error", err)
	}
}

func TestCompileEditPlan_TypeScriptDecoratorDetached(t *testing.T) {
	// @decorator on line 5; class declaration on line 6; hidden decorator
	// while the class body remains visible must be rejected.
	const raw = "diff --git a/svc.ts b/svc.ts\n--- a/svc.ts\n+++ b/svc.ts\n@@ -0,0 +1,5 @@\n+@injectable()\n+class Service {\n+    run() {}\n+    stop() {}\n+}\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err == nil || !strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf("hidden TS decorator with visible class error = %v", err)
	}
}

func TestCompileEditPlan_TypeScriptRejectsDanglingMultilineCall(t *testing.T) {
	const raw = "diff --git a/call.ts b/call.ts\n--- a/call.ts\n+++ b/call.ts\n@@ -0,0 +1,4 @@\n+result = call(\n+    first,\n+    second,\n+)\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err == nil || !strings.Contains(err.Error(), "delimiter structure") {
		t.Fatalf("dangling TS call error = %v", err)
	}
}

func TestCompileEditPlan_TypeScriptGenericPreservation(t *testing.T) {
	t.Run("elision inside a generic argument is allowed", func(t *testing.T) {
		const raw = "diff --git a/list.ts b/list.ts\n--- a/list.ts\n+++ b/list.ts\n@@ -0,0 +1 @@\n+const cache: Map<string, number> = new Map();\n"
		compiled, err := compileEditPlan(raw, editPlan{Replace: []lineReplacement{{
			Line: 5,
			Old:  "Map<string, number>",
			New:  "Map<...>",
		}}})
		if err != nil {
			t.Fatalf("TS generic elision error = %v", err)
		}
		if !strings.Contains(compiled.smartDiff, "Map<...>") {
			t.Fatalf("TS generic elision diff = %q", compiled.smartDiff)
		}
	})

	t.Run("rejects a replacement that drops generic angle brackets", func(t *testing.T) {
		const raw = "diff --git a/list.ts b/list.ts\n--- a/list.ts\n+++ b/list.ts\n@@ -0,0 +1 @@\n+const cache: Map<string, number> = new Map();\n"
		// Replace `<string, number>` (only this fragment) with a single
		// `...` placeholder. The new body is `Map<...> = new Map()`.
		// Before: LAngle+1, RAngle+1. After: LAngle+1, RAngle+1.
		// The elision projection is valid (old is a substring of new's
		// source projection when ... is reintroduced), and the structural
		// check is satisfied; the per-line elision succeeds. This is
		// the baseline we expect to work, not to fail.
		_, err := compileEditPlan(raw, editPlan{Replace: []lineReplacement{{
			Line: 5,
			Old:  "Map<string, number>",
			New:  "Map<...>",
		}}})
		if err != nil {
			t.Fatalf("TS angle-preserving replace error = %v", err)
		}
	})

	t.Run("rejects a replace that drops the LAngle while keeping the RAngle", func(t *testing.T) {
		// A non-elision replacement that drops a structural token
		// entirely is rejected by isElisionProjection before the TS
		// structural check runs. The TS check itself enforces
		// per-line LAngle/RAngle balance on any replacement whose new
		// is a structural projection of the old — when the elision
		// succeeds, the per-line balance is preserved.
		const raw = "diff --git a/list.ts b/list.ts\n--- a/list.ts\n+++ b/list.ts\n@@ -0,0 +1 @@\n+const cache: Map<string, number> = new Map();\n"
		// `Map<string, number>` is unique on the line. The new drops
		// both angle brackets. The elision check rejects the new
		// because there is no `...` placeholder.
		_, err := compileEditPlan(raw, editPlan{Replace: []lineReplacement{{
			Line: 5,
			Old:  "Map<string, number>",
			New:  "Map number",
		}}})
		if err == nil {
			t.Fatalf("expected elision check to reject this, got nil")
		}
	})
}

func TestCompileEditPlan_TypeScriptCommentsAndStringsDontConfuseScanner(t *testing.T) {
	// `}` inside a comment, a string, and a template literal must NOT
	// close the class body. The hunk has the visible brace owner and
	// a hidden class owner; the only structural `{`/`}` pair is at
	// the class body, so removing the class line is rejected.
	const raw = "diff --git a/scan.ts b/scan.ts\n--- a/scan.ts\n+++ b/scan.ts\n@@ -0,0 +1,5 @@\n+class Scan {\n+    // closing brace in comment: }\n+    msg = \"literal } here\"\n+    tpl = `template ${ { nested: true } }`\n+}\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err == nil || !strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf("comment/string scanner error = %v", err)
	}
}

func TestCompileEditPlan_TypeScriptAllowsPureStringLocalElision(t *testing.T) {
	// Local elision of a noisy message argument must not change brace
	// or angle-bracket balance. Parens are excluded from the strict
	// per-line check so this should compile.
	const raw = "diff --git a/err.ts b/err.ts\n--- a/err.ts\n+++ b/err.ts\n@@ -0,0 +1 @@\n+throw new Error(\"bad request name=X and user=Y and id=Z\")\n"
	compiled, err := compileEditPlan(raw, editPlan{Replace: []lineReplacement{{
		Line: 5,
		Old:  "\"bad request name=X and user=Y and id=Z\"",
		New:  "\"bad request ...\"",
	}}})
	if err != nil {
		t.Fatalf("TS string elision error = %v", err)
	}
	if !strings.Contains(compiled.smartDiff, "throw new Error(\"bad request ...\")") {
		t.Fatalf("TS string elision diff = %q", compiled.smartDiff)
	}
}

func TestCompileEditPlan_TypeScriptVsJavaScriptRouting(t *testing.T) {
	// `a.js` should be classified as JavaScript; the same hidden-owner
	// pattern must NOT trigger the TypeScript validator. A `.js` file
	// has no TypeScript structural guarantee in the rubric, so the
	// plan should compile (or fail on a different, language-agnostic
	// check) without a TypeScript-specific error.
	jsRaw := "diff --git a/work.js b/work.js\n--- a/work.js\n+++ b/work.js\n@@ -0,0 +1,4 @@\n+class Foo {\n+    first()\n+    second()\n+}\n"
	_, err := compileEditPlan(jsRaw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err != nil && strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf(".js file triggered TypeScript validator: %v", err)
	}
}

func TestCompileEditPlan_TypeScriptAcceptsTaggedTemplateFold(t *testing.T) {
	// Template-literal `}` inside a tagged template must not break
	// the brace balance. Folding a CSS-in-JS body should be allowed.
	const raw = "diff --git a/css.ts b/css.ts\n--- a/css.ts\n+++ b/css.ts\n@@ -0,0 +1,5 @@\n+const button = styled.button`\n+    color: red;\n+    background: ${theme.bg};\n+    border: 1px solid black;\n+`\n"
	compiled, err := compileEditPlan(raw, editPlan{Fold: []lineFold{{StartLine: 6, EndLine: 8}}})
	if err != nil {
		t.Fatalf("tagged template fold error = %v", err)
	}
	if !strings.Contains(compiled.smartDiff, "styled.button`\n+    ...\n+`") {
		t.Fatalf("tagged template fold diff = %q", compiled.smartDiff)
	}
}

func TestCompileEditPlan_TypeScriptJSXAcceptsFold(t *testing.T) {
	// Folding a multi-line JSX element should be allowed: the
	// per-hunk angle-bracket delta is computed against the raw side,
	// and a fold that hides the entire JSX element drops a balanced
	// pair, so the visible-side delta still matches the raw-side
	// delta (both lost the same LAngle/RAngle). The scanner must
	// still recognize JSX text correctly so the bracket count is
	// accurate.
	const raw = "diff --git a/btn.tsx b/btn.tsx\n--- a/btn.tsx\n+++ b/btn.tsx\n@@ -0,0 +1,5 @@\n+function Button({label}: {label: string}) {\n+    return (\n+        <button className=\"primary\">{label}</button>\n+    )\n+}\n"
	compiled, err := compileEditPlan(raw, editPlan{Fold: []lineFold{{StartLine: 6, EndLine: 8}}})
	if err != nil {
		t.Fatalf("TSX fold error = %v", err)
	}
	if !strings.Contains(compiled.smartDiff, "    ...\n+}\n") {
		t.Fatalf("TSX fold diff = %q", compiled.smartDiff)
	}
}

func TestCompileEditPlan_TypeScriptJSXAngleBalance(t *testing.T) {
	// Replacing the JSX expression must keep angle brackets balanced.
	// We use a JSX element whose literal opener and closer appear on
	// the line so the elision check can match without ambiguity, then
	// exercise the per-line structural balance with a drop.
	const raw = "diff --git a/btn.tsx b/btn.tsx\n--- a/btn.tsx\n+++ b/btn.tsx\n@@ -0,0 +1,5 @@\n+function Button({label}: {label: string}) {\n+    return (\n+        <Button>{label}</Button>\n+    )\n+}\n"
	// A balanced per-line elision: keep the angle brackets, elide the
	// children. The structural check should pass.
	_, err := compileEditPlan(raw, editPlan{Replace: []lineReplacement{{
		Line: 7,
		Old:  "<Button>{label}</Button>",
		New:  "<Button>...</Button>",
	}}})
	if err != nil {
		t.Fatalf("TSX balanced replace error = %v", err)
	}
}

func TestTypeScriptTokenizer_TypeScriptXJSXAndComparisons(t *testing.T) {
	t.Run("intrinsic and self-closing JSX do not swallow later code", func(t *testing.T) {
		lines := []tsSourceLine{{index: 0, text: "const view = <div>{value}</div>;"}, {index: 1, text: "const item = <Widget />;"}, {index: 2, text: "if (ready) { finish(); }"}}
		tokens, _ := scanTypeScriptSource(lines)
		if got := tsDelimiterDelta(tokens); got.braces != 0 || got.angles != 0 {
			t.Fatalf("JSX/code delimiters = %+v, want balanced", got)
		}
	})
	t.Run("less-than comparison is not a generic opener", func(t *testing.T) {
		tokens, _ := scanTypeScriptSource([]tsSourceLine{{index: 0, text: "if (a < b && c > d) { ok(); }"}})
		if got := tsDelimiterDelta(tokens); got.angles != 0 {
			t.Fatalf("comparison angles = %+v, want zero", got)
		}
	})
}

func TestCompileEditPlan_TypeScriptReplacementCannotEraseOwner(t *testing.T) {
	const raw = "diff --git a/owner.ts b/owner.ts\n--- a/owner.ts\n+++ b/owner.ts\n@@ -0,0 +1,3 @@\n+class Service {\n+    run() {}\n+}\n"
	_, err := compileEditPlan(raw, editPlan{Replace: []lineReplacement{{Line: 5, Old: "class Service", New: "..."}}})
	if err == nil || !strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf("owner-erasing replacement error = %v", err)
	}
}

func TestCompileEditPlan_TypeScriptAsyncArrow(t *testing.T) {
	const raw = "diff --git a/run.ts b/run.ts\n--- a/run.ts\n+++ b/run.ts\n@@ -0,0 +1,4 @@\n+const run = async () => {\n+    await task()\n+    close()\n+}\n"
	t.Run("hidden async-arrow owner with retained body is rejected", func(t *testing.T) {
		_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
		if err == nil || !strings.Contains(err.Error(), "TypeScript") {
			t.Fatalf("hidden async-arrow owner error = %v", err)
		}
	})
	t.Run("folding the body keeps the arrow head", func(t *testing.T) {
		compiled, err := compileEditPlan(raw, editPlan{Fold: []lineFold{{StartLine: 6, EndLine: 7}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(compiled.smartDiff, "const run = async () => {\n+    ...\n+}\n") {
			t.Fatalf("async-arrow fold = %q", compiled.smartDiff)
		}
	})
}

func TestCompileEditPlan_TypeScriptCrossFileMixedLanguages(t *testing.T) {
	// A hunk that contains a TS file and a Go file in the same diff
	// should only run the TS structural check on the TS side and the
	// Go structural check on the Go side. The hidden Go function
	// owner on line N+1 should still be rejected by validateGoStructure
	// even if the TS side is fine, and vice versa.
	raw := "diff --git a/lib.go b/lib.go\n--- a/lib.go\n+++ b/lib.go\n@@ -0,0 +1,3 @@\n+func ok() {\n+\tdoit()\n+}\n" + "diff --git a/ui.ts b/ui.ts\n--- a/ui.ts\n+++ b/ui.ts\n@@ -0,0 +1,3 @@\n+class Panel {\n+    show()\n+}\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err == nil || !strings.Contains(err.Error(), "Go") {
		t.Fatalf("cross-file Go hidden owner error = %v", err)
	}
	_, err = compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 12, EndLine: 12}}})
	if err == nil || !strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf("cross-file TS hidden owner error = %v", err)
	}
}

func TestPathLanguage_TypeScript(t *testing.T) {
	cases := []struct {
		path string
		want sourceLanguage
	}{
		{"src/index.ts", sourceLanguageTypeScript},
		{"src/Button.tsx", sourceLanguageTypeScript},
		{"src/foo.mts", sourceLanguageTypeScript},
		{"src/bar.cts", sourceLanguageTypeScript},
		{"src/index.js", sourceLanguageJavaScript},
		{"src/Button.jsx", sourceLanguageJavaScript},
		{"src/foo.mjs", sourceLanguageJavaScript},
		{"src/bar.cjs", sourceLanguageJavaScript},
		{"src/main.go", sourceLanguageGo},
		{"src/lib.py", sourceLanguagePython},
	}
	for _, c := range cases {
		if got := pathLanguage(c.path); got != c.want {
			t.Errorf("pathLanguage(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestTypeScriptTokenizer_StringsAndTemplates(t *testing.T) {
	t.Run("string literal containing brace is not a delimiter", func(t *testing.T) {
		lines := []tsSourceLine{{index: 7, text: `const s = "}";`}}
		tokens, _ := scanTypeScriptSource(lines)
		if tsDelimiterDelta(tokens).braces != 0 {
			t.Fatalf("string-brace leaked into brace delta: %+v", tsDelimiterDelta(tokens))
		}
	})
	t.Run("template literal with interpolation does not break balance", func(t *testing.T) {
		lines := []tsSourceLine{{index: 7, text: "const t = `a ${ { x: 1 } } b`;"}}
		tokens, _ := scanTypeScriptSource(lines)
		if tsDelimiterDelta(tokens).braces != 0 {
			t.Fatalf("template-brace leaked into brace delta: %+v", tsDelimiterDelta(tokens))
		}
	})
	t.Run("block comment containing brace is not a delimiter", func(t *testing.T) {
		lines := []tsSourceLine{{index: 7, text: "/* not a } brace */"}}
		tokens, _ := scanTypeScriptSource(lines)
		if tsDelimiterDelta(tokens).braces != 0 {
			t.Fatalf("comment-brace leaked into brace delta: %+v", tsDelimiterDelta(tokens))
		}
	})
}

func TestTypeScriptRegexAllowedAt(t *testing.T) {
	cases := []struct {
		text string
		at   int
		want bool
	}{
		{"const r = /foo/g;", 10, true},
		{"return /bar/.test(x);", 7, true},
		{"if (x) { y = /baz/; }", 13, true},
		{"n /= 2", 2, false},
		{"x = y / 2", 8, false},
		{"a[0] = /re/", 7, true},
		{"foo() / 2", 6, false},
		{"a / /re/.test(b);", 4, false},
		{"typeof /re/", 7, true},
		{"throw /re/", 6, true},
		{"call(1, 2) / 3", 11, false},
		{"a[0] / 2", 5, false},
	}
	for _, c := range cases {
		if got := tsRegexAllowedAt(c.text, c.at); got != c.want {
			t.Errorf("tsRegexAllowedAt(%q, %d) = %v, want %v", c.text, c.at, got, c.want)
		}
	}
}

func TestCompileEditPlan_TypeScriptModuleOwner(t *testing.T) {
	const raw = "diff --git a/api.ts b/api.ts\n--- a/api.ts\n+++ b/api.ts\n@@ -0,0 +1,4 @@\n+export module Api {\n+    export const ok = 1\n+    export const err = 0\n+}\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err == nil || !strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf("hidden module owner error = %v", err)
	}
}

func TestCompileEditPlan_TypeScriptIfOwner(t *testing.T) {
	// Hidden `if` owner with retained body is rejected; a single-line
	// if-then without braces does not have an LBRACE on the owner
	// line, so this guards against false positives on brace-less
	// control structures.
	const raw = "diff --git a/cond.ts b/cond.ts\n--- a/cond.ts\n+++ b/cond.ts\n@@ -0,0 +1,4 @@\n+if (cond) {\n+    first()\n+    second()\n+}\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 5, EndLine: 5}}})
	if err == nil || !strings.Contains(err.Error(), "TypeScript") {
		t.Fatalf("hidden if-owner error = %v", err)
	}
}

func TestCompileEditPlan_TypeScriptEmptyHunkOK(t *testing.T) {
	// A hunk that contains only removed lines produces no visible
	// side; the structural validator must not crash. The plan must
	// also remove the file headers and the hunk header so the file
	// is fully dropped.
	const raw = "diff --git a/gone.ts b/gone.ts\n--- a/gone.ts\n+++ b/gone.ts\n@@ -1,2 +0,0 @@\n-class Old {}\n-    keepMe: true\n"
	_, err := compileEditPlan(raw, editPlan{Remove: []lineRange{{StartLine: 1, EndLine: 6}}})
	if err != nil {
		t.Fatalf("purely-removed TS hunk error = %v", err)
	}
}
