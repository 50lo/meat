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
load-bearing). The agent does **not** regenerate the displayed diff wholesale. It
submits inclusive original-line ranges to remove, contiguous same-polarity source
ranges to collapse into machine-generated, indentation-preserving `...` rows,
and exact source substrings for local single-line elisions. Before validation,
Meat deterministically derives a mandatory source-coordinate removal plan for
imports/includes/requires/use declarations and merges it with the model plan.
Meat validates and previews the merged plan, then applies it locally to the
immutable input. The agent never authors fold text or rewrites the displayed
diff wholesale: every non-ellipsis character in a rewritten span—and every
unchanged byte elsewhere—came from the original diff.

For sizeable plans the agent can call `preview_plan` to inspect the projected
reading diff and retention statistics before submission. A high-retention
submission gets one advisory refinement turn, with the first valid draft kept as
a safe fallback. Retention is never a hard quota.

The compiler removes import/include/require/use scaffolding automatically and
hermetically — including package substitutions, complete multiline blocks,
import-only hunks/files and their framing, and recognized import rows embedded
in test source strings. This is a machine invariant, not a request to the LLM:
identity plans, previews, submissions, refinement drafts, and fallbacks are all
import-free. On exact moves this mandatory hiding wins first: an aligned
counterpart is hidden too even when its file extension classifies the row
differently, and compiler-owned Python suite placeholders do not count as
model folds. Symmetry remains strict for model-authored compression of the
remaining behavioral rows. Folds that cross from mandatory import rows into
behavioral rows are rejected.

The reading diff deliberately preserves the original hunk headings for
orientation rather than recomputing an applicable patch. After elision, hunk
line counts may therefore be stale.

The result is printed with a one-line summary. A machine-computed elision
manifest (`# kept 12/240 changed lines in 3/7 files`) shows at a glance how much
you are *not* reading; the counts come from the locally applied result, not from
numbers reported by the LLM.

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

# staged (index) or unstaged working-tree changes
go run meat.dev/cmd/meat -staged
go run meat.dev/cmd/meat -w

# machine-readable output for CI / bots
go run meat.dev/cmd/meat -json <sha>

# abridge any diff piped on stdin
git show <sha> | go run meat.dev/cmd/meat
git diff main...HEAD | go run meat.dev/cmd/meat

# help
go run meat.dev/cmd/meat -h
```

On an **exe.dev VM** with an attached `llm` integration, `meat` uses the managed
LLM gateway automatically — no API key needed. Otherwise, OpenAI models use
`OPENAI_API_KEY` (optional `OPENAI_BASE_URL`) and Claude models use
`ANTHROPIC_API_KEY` (optional `ANTHROPIC_BASE_URL`). Select a model with
`MEAT_MODEL` or `-model`.

The default is `gpt-5.6-sol`. OpenAI requests use the Responses API with medium
reasoning effort, stateless encrypted-reasoning replay across tool turns, and
streaming transport. Embedders constructing `OpenAIModel` directly can override
`ReasoningEffort`.

Results are cached under `~/.meat`, keyed by the SHA-256 of the model, the
rubric/compiler-protocol version, and the diff contents. Re-running on an
unchanged diff is instant; editing the diff, switching models, or upgrading
meat to a tuned rubric or changed compiler invariant recomputes. Pass
`-no-cache` to force a recompute, set `MEAT_CACHE` to use a different
directory, or `MEAT_CACHE=` to disable caching.

On an interactive terminal `meat` renders like `git show`: the diff is colored
with your git diff colors and shown through your git pager. Piped or redirected
output stays plain text. Honors `GIT_PAGER`/`core.pager`/`PAGER` and the
`color.diff.*` config.

With no stdin pipe, `meat` reads the top commit (`git show HEAD`) of the repo
you're in and summarizes it. Merge commits show their first-parent diff (what
merging the branch changed on the target), not nothing.

While the agent works, an interactive terminal gets a single self-overwriting
status line on stderr (`meat: thinking (turn 2)`, `meat: read_file foo.go`);
any redirection (`meat > file`, `-json`) suppresses it. When done, meat prints
token usage and elapsed time to stderr.

## Layout

- `meat.dev/cmd/meat` — `package main`, the CLI. Stdlib only.
- `meat.dev/meat` — the reusable guts: the agent loop (`Abridge`), the rubric,
  the read-only tools, and a provider-agnostic `Model` interface plus built-in
  stdlib `OpenAIModel` and `AnthropicModel` implementations.

The `Model` interface keeps the package embeddable: the CLI selects OpenAI
Responses for non-Claude model IDs and Anthropic Messages for `claude-*`, while
other programs (e.g. Shelley) can supply their own `Model` by adapting an
existing LLM client — no shared LLM dependency required.

## Tuning

Edit `meat/rubric.go` to tune model judgment. Hard source-derived invariants
such as mandatory import removal live in the compiler (`meat/imports.go`) and
must be covered by hermetic tests rather than rubric examples alone.

The checked-in Python corpus also gates reading quality, not just renderer
correctness. Its source-coordinate plans must preserve semantic anchors while
staying within deterministic absolute budgets: **194/411 changed rows,
340/722 physical rows, and 14,337/29,739 bytes** across the three fixtures.
Those plans deliberately contain no import coordinates—the compiler owns import
hiding—and the pytest relocation must satisfy exact move symmetry. These
hermetic goldens are the hard gates: snapshot bytes, semantic anchors, and
budgets are deterministic. The opt-in `MEAT_E2E=1` tests are costed, stochastic
rubric smoke tests with empirically calibrated retention ceilings and stable
semantic minima; they are not expected to reproduce the hand-authored plans.
When tuning, prefer one representative rename/call-site anchor, remove context
that adds no orientation, compress repetitive test setup/assertions, and retain
required setup, contracts, security/compatibility caveats, conditions,
transformations, effects, distinctive stimuli, and outcomes. See
`meat/testdata/python/README.md` before updating snapshots or budgets.
