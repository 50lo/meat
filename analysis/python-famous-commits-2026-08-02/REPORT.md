# Famous Python commits: original diff vs live Meat

Run on **August 2, 2026** with the default model, `claude-opus-4-8`, using
`-no-cache`. Each model run happened from a checkout of the upstream commit, so
Meat's read-only tools could inspect the surrounding project. The input was the
commit's Python diff only; non-Python changelog, contributor, and configuration
files were intentionally excluded.

The exact live responses are checked in beside this report as `.live.json`; the
`smart_diff` fields are also extracted as `.live.diff` for convenient reading.
The existing `.golden.diff` files are hand-reviewed, compiler-rendered reference
outputs, not the live model output.

Snapshot provenance: Meat source commit
`1760979d508f94d9d1e69b863272a0f51d44af10`, rubric hash
`42593d94b5993cea`, and the exact upstream revisions named below. The command
pattern was to build `./cmd/meat`, make the upstream checkout the process working
directory, then run:

```sh
/path/to/meat -no-cache -json < /path/to/python-only-commit.diff
```

The JSON files are the captured stdout. Token counts are inside them; elapsed
wall times are recorded below. Because `-no-cache` invokes a stochastic model,
the setup is replayable but the exact prose and plan are not expected to be
byte-for-byte reproducible.

## Headline

Meat consistently found the semantic center of all three commits. It also
consistently spent too much of its reading budget on repetitive plumbing and,
in Django's case, hid some of the best test stimuli while retaining mechanical
call-site churn.

| project / commit | original changed rows | live shown | changed rows elided | original physical rows | live physical rows | byte reduction | reviewed reference |
|---|---:|---:|---:|---:|---:|---:|---:|
| Django `526b1b414d8e` | 178 | 112 (62.9%) | 66 (37.1%) | 329 | 231 (70.2%) | 34.6% | 89 changed / 155 rows |
| Flask `c17f37939073` | 124 | 96 (77.4%) | 28 (22.6%) | 234 | 178 (76.1%) | 23.4% | 63 changed / 120 rows |
| pytest `b4e846616cbb` | 109 | 83 (76.1%) | 26 (23.9%) | 159 | 123 (77.4%) | 29.3% | 42 changed / 65 rows |
| **Corpus** | **411** | **291 (70.8%)** | **120 (29.2%)** | **722** | **532 (73.7%)** | **30.1%** | **194 changed / 340 rows** |

The live runs used 471,501 aggregate input tokens and 16,144 output tokens over
233.8 seconds: Django 108.7 seconds, Flask 80.3 seconds, and pytest 44.8
seconds. The high input count includes the multi-turn tool-assisted repo
inspection, not only the three diff payloads.

## Django: header token normalization and cache behavior

**Commit:** `526b1b414d8e215bf627b5722df12a09346dbf6b`, committed June 8,
2026, “Refs CVE-2026-48587 -- Added helper to properly split header values.”

- [Original Python diff](../../meat/testdata/python/django-526b1b414d8e.diff)
- [Live Meat output](django.live.diff) · [full JSON](django.live.json)
- [Reviewed reference](../../meat/testdata/python/django-526b1b414d8e.golden.diff)

### What the commit does

It replaces `cc_delim_re.split(...)` with a shared
`split_header_value(value, sep=",")` generator that strips every token and drops
empty ones. That fixes first/last-token whitespace that the old delimiter regex
did not consume. The effects reach ETag suppression, `max-age` parsing, `Vary`
normalization, cache-key construction, and ETag parsing. The helper explicitly
warns that it is only for token-list headers, not headers such as `Set-Cookie`
whose quoted values may contain commas.

### What Meat got right

- Kept the complete helper, including the token-list contract, quoted-value
  caveat, `value.split(sep)`, `part.strip()`, and empty-token guard.
- Kept the genuinely new `newheaders = [newheader.strip() ...]` transformation.
- Kept representative production effects in middleware, cache handling, and
  ETag parsing.
- Removed all import scaffolding, retained a leading-space `max-age` example and
  one whitespace-padded `Vary` example, and folded the rest of the tables.
- Kept the cache-key inequality outcome, which exposes the most consequential
  behavior.

### What is still boring

A further **22–27 changed rows and roughly 75 physical rows** can disappear
without weakening the explanation:

1. After one representative migration, the `patch_cache_control`, `get_max_age`,
   and `learn_cache_key` hunks are nearly identical
   `cc_delim_re.split(X)` → `split_header_value(X)` substitutions.
2. The local test helper change at `tests/cache/tests.py:2445` is another spelling
   of the same migration, not a new behavior.
3. `self.assertIsNotNone(key_a)` and `self.assertIsNotNone(key_b)` are subsumed by
   the retained `self.assertNotEqual(key_a, key_b)`.
4. Four adjacent, identically indented `...` rows appear inside one `headers`
   table. Once intervening comments are removed, those folds should coalesce to
   one marker. The live diff also retains 14 bare changed blank rows.
5. `test_custom_sep` repeats the entire `test_basic` harness; only one distinctive
   semicolon-delimited case and the `sep=";"` call are needed.

### Wrong-budget problem

The rerun improved stimulus retention, but often selected the cheapest rather
than the most explanatory row. Both helper tables retain `("", [])`, making the
comma and semicolon tests look identical; neither demonstrates stripping or the
separator boundary. The `Vary` table retains a basic padded token but hides every
wildcard and tab case. Meat should trade repetitive call-site hunks for a padded
wildcard, a tab case, a comma/semicolon boundary, and the custom-separator
outcome.

**Suggested live target:** about **85–90/178 changed rows**, around 155 physical
rows. That is 20–24% fewer changed rows and about 33% fewer physical rows than
this live run, while preserving more useful stimulus than the live output
currently shows.

## Flask: session access tracking moves to the request context

**Commit:** `c17f379390731543eea33a570a47bd4ef76a54fa`, February 18, 2026,
“request context tracks session access.” Its `CHANGES.rst` entry links the change
to GHSA-68rp-wp8r-4726 and calls out key-only operations such as `in` and `len`.

- [Original Python diff](../../meat/testdata/python/flask-c17f37939073.diff)
- [Live Meat output](flask.live.diff) · [full JSON](flask.live.json)
- [Reviewed reference](../../meat/testdata/python/flask-c17f37939073.golden.diff)

### What the commit does

`RequestContext.session` becomes a property over `_session`. Accessing it marks
the backing session as accessed. Internal lifecycle and response-saving paths
use `_session` deliberately so they do not create a false access. Tracking is
removed from three `SecureCookieSession` dictionary methods, the mixin default
flips from `accessed = True` to `False`, template-context behavior changes, and
tests assert the resulting `Vary: Cookie` behavior.

### What Meat got right

- Preserved the property contract and the exact effect:
  `self._session.accessed = True`.
- Preserved deliberate bypasses in save, copy, and push paths; these are semantic,
  not a cosmetic private-field rename.
- Preserved the new `accessed = False` default, public type narrowing, template
  proxy caveat, and representative `Vary: cookie` outcomes.
- Correctly collapsed the three repeated `__getitem__`, `get`, and `setdefault`
  overrides to one `-    ...` marker.
- Kept the useful docstring sentence connecting access tracking to
  `Vary: Cookie`, while import-only churn stayed hidden.

### What is still boring

The rerun improved substantially, but still retains **77.4%** of changed rows.

1. **Eight blank changed rows** survive as bare `+` or `-` lines.
2. Sixteen non-blank rows from the superseded old `test_session` body remain,
   including duplicate old `accessed`/`modified` assertions and obsolete route
   setup. The test rename already signals a rewrite.
3. The two new access-tracking docstrings consume 12 changed rows. Keep the
   property contract and the one `Vary: Cookie` sentence; fold the rest.
4. `assert rv.text == "value set"` and `assert rv.text == "42"` repeat the view
   bodies retained a few lines above.
5. Repeated exception-handler, constructor-call, lifecycle-comment, and
   function-boundary context can be removed once hunk orientation is clear.

### Wrong-budget problem

The live output retains the dead old assertions while folding the decisive new
outcome matrix. All four `request_ctx._session.accessed` assertions are hidden,
three of four `modified` assertions are hidden, and the final no-`Vary` outcome
is hidden. It also drops the old `-    accessed = True` side, so a reviewer sees
`+    accessed = False` as an addition rather than a compatibility-relevant
inversion. Trade the old test body and most doc prose for the old/new default
pair plus the POST and read-only GET `accessed`/`modified` outcomes.

**Suggested live target:** **68–80/124 changed rows** (55–65%), no more than about
150 physical rows. This commit is semantically dense, but not 77%-dense.

## pytest: warning-filter lifetime moves ahead of `pytest_configure`

**Commit:** `b4e846616cbb0ba74dc548f7066b09d820f5dc05`, July 22, 2026,
“Apply warning filters to `pytest_configure` (#14760).” It fixes pytest issue
#10128.

- [Original Python diff](../../meat/testdata/python/pytest-b4e846616cbb.diff)
- [Live Meat output](pytest.live.diff) · [full JSON](pytest.live.json)
- [Reviewed reference](../../meat/testdata/python/pytest-b4e846616cbb.golden.diff)

### What the commit does

The configured-warning context is moved from `_pytest.warnings` into
`Config._catch_configured_warnings`. `_do_configure` enters it before the
historic `pytest_configure` hook call, guarded by the warnings plugin's presence.
This fixes hook-order behavior: even a `tryfirst=True` hook now runs under the
configured filters. The warnings plugin's own `pytest_configure` becomes much
smaller, and a parametrized regression test checks both hook orders.

### What Meat got right

- Kept the new method's location/import-cycle explanation.
- Kept the warnings-plugin guard and, critically, the context showing it is
  entered before `pytest_configure.call_historic`.
- Collapsed the exact moved warning-default body on both sides with
  indentation-correct ellipses while retaining
  `apply_warning_filters(config_filters, cmdline_filters)` as a closing anchor.
  This is the strongest example of Meat's move symmetry working.
- Kept the new call site, old lifecycle removal, `tryfirst` stimulus, configured
  filter, warning emission, and no-warning outcomes.
- Removed import churn and generated-conftest imports.

### What is still boring

About **5–9 changed rows** can go with no semantic loss:

1. The removed `config_filters` and `cmdline_filters` bindings duplicate the
   destination bindings and sit outside the detected exact-move range. Drop
   those two rows, but keep the paired `warnings.catch_warnings(...)` body
   symmetric on both move endpoints.
2. `config.addinivalue_line(...)` is shown in full on both sides merely to express
   a dedent. Keep the final form and fold the removed form.
3. Empty added lines and `pytester.makepyfile("def test_it(): pass")` are low-value
   harness details.

The new run fixes the earlier payload over-elision, but now hides the
`record=False` rationale on both sides. Keeping the destination-side copy would
explain why warnings are not recorded during configuration and why the context
is only useful for `error` filters; its old copy is outside the detected move and
can stay folded.

**Suggested live target:** about **76–80/109 changed rows** (70–73%). The remaining
gap is unmatched prologue, dedent-only duplication, and minor test padding; the
exact moved body itself should stay symmetric.

## Cross-commit recommendations

In priority order:

1. **Deterministically remove blank-only changed rows.** They are never review
   evidence and are especially costly in Python test rewrites.
2. **Prefer current outcomes over superseded assertions.** Flask keeps the dead
   test body while folding every `accessed` assertion in the replacement test.
3. **Coalesce adjacent same-indent ellipsis rows.** Django emits four consecutive
   fold markers inside one table after the intervening comments disappear.
4. **Compress dedent-only duplicates and unmatched move framing.** For detected
   exact moves, keep the aligned behavioral body symmetric; trim only rows
   outside the reported pair unless both endpoints receive the same fold/elision.
5. **Retain distinctive table stimuli, not whole harnesses.** Keep wildcard,
   tabs, delimiter boundaries, lifecycle order, and outcome; fold loop/setup
   machinery.
6. **After one representative call-site anchor, drop mechanical migrations.**
   Django spends dozens of rows proving the same rename.
7. **Tighten live retention ceilings only after the deterministic/rubric fixes.**
   Current ceilings tolerate outputs that are technically abridged but still
   much larger than the reviewed references.
