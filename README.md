# meat

Abridge a code diff into a **reading diff**: the same change, rewritten to keep
only the parts a senior reviewer actually needs to read.

Reviewing *good* code in diff form is mostly about understanding the high-level
change, not sweating details — it compiles, the tests pass. You don't need to
read the exact spelling of an error message, the type conversions threaded
through a batch field copy, a zero value added to a return because a new return
value was introduced elsewhere, or **generated code**. You need: *what changed,
where did data come from, where did it go, what new control flow appeared.*

`meat` feeds the whole diff to an LLM agent (with read-only `read_file`/`grep`
access to the surrounding repo, so it can use clues to decide what's
load-bearing) and prints the abridged diff plus a one-line summary.

## Install / run

```bash
# install the `meat` binary
go install meat.dev/cmd/meat@latest

# summarize the most recent commit in the current repo
go run meat.dev/cmd/meat

# summarize a specific commit or revision
go run meat.dev/cmd/meat <sha>

# diff across a commit range
go run meat.dev/cmd/meat <sha1>..<sha2>
go run meat.dev/cmd/meat main...HEAD

# abridge any diff piped on stdin
git show <sha> | go run meat.dev/cmd/meat
git diff main...HEAD | go run meat.dev/cmd/meat

# help
go run meat.dev/cmd/meat -h
```

On an **exe.dev VM** with an attached `llm` integration, `meat` uses the managed
LLM gateway automatically — no API key needed. Otherwise it requires
`ANTHROPIC_API_KEY`. Optional: `ANTHROPIC_BASE_URL`, `MEAT_MODEL` (or `-model`).
The default model is Claude Opus 4.8.

Results are cached under `~/.meat`, keyed by the SHA-256 of the model plus the
diff contents. Re-running on an unchanged diff is instant; any edit to the diff
changes the key and recomputes. Pass `-no-cache` to force a recompute, set
`MEAT_CACHE` to use a different directory, or `MEAT_CACHE=` to disable caching.

On an interactive terminal `meat` renders like `git show`: the diff is colored
with your git diff colors and shown through your git pager. Piped or redirected
output stays plain text. Honors `GIT_PAGER`/`core.pager`/`PAGER` and the
`color.diff.*` config.

With no stdin pipe, `meat` reads the top commit (`git show HEAD`) of the repo
you're in and summarizes it.

## Layout

- `meat.dev/cmd/meat` — `package main`, the CLI. Stdlib only.
- `meat.dev/meat` — the reusable guts: the agent loop (`Abridge`), the rubric,
  the read-only tools, and a provider-agnostic `Model` interface plus a built-in
  stdlib `AnthropicModel`.

The `Model` interface keeps the package embeddable: the CLI uses the built-in
Anthropic backend, while other programs (e.g. Shelley) supply their own `Model`
by adapting an existing LLM client — no shared LLM dependency required.

## Tuning

Edit `meat/rubric.go`. It's a single string with concrete worked examples — the
one knob to tune as new categories of noise show up.
