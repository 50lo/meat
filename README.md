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
