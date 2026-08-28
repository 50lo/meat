# TypeScript abridging golden

This fixture is a synthetic three-file TypeScript/TSX diff covering a changed
result contract, retryability and warning behavior, updated tests, and rendered
warnings. It has no claimed upstream commit provenance; the object IDs in the
diff are fixture-local.

The source-coordinate plan leaves import hiding to the compiler. TypeScript
imports, including `import type`, are removed automatically, while public
`export type` declarations remain available as contract evidence.

| fixture | raw changed | golden changed | raw physical rows | golden physical rows | raw bytes | golden bytes |
|---|---:|---:|---:|---:|---:|---:|
| `ts-result-warnings` | 118 | 60 | 198 | 138 | 6,190 | 4,151 |

The deterministic hard gate is `TestTypeScriptGoldenCommits`. It checks the
source-coordinate plan, exact rendered bytes, semantic anchors, automatic
import removal, one or more balanced folds, and absolute ceilings of 60 changed
rows, 140 physical rows, and 4,500 bytes.

Regenerate the snapshot only after reviewing the plan and assertions:

```sh
UPDATE_GOLDEN=1 go test ./meat -run TestTypeScriptGoldenCommits
```
