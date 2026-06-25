package meat

import (
	"context"
	"os"
	"strings"
	"testing"
)

// TestRubric_ImportChoices is an opt-in, end-to-end check of the import-elision
// rubric against a real LLM. It is gated behind MEAT_E2E=1 because it makes a
// live (non-deterministic, costed) model call; the default `go test` run stays
// hermetic. Run it with:
//
//	MEAT_E2E=1 go test ./meat -run TestRubric_ImportChoices
//
// It encodes the behavior we want the rubric to produce:
//   - a meaningful package CHOICE (math/rand -> crypto/rand) survives, while the
//     boring companion import (encoding/hex) added purely because kept code uses
//     it is dropped;
//   - plain stdlib-to-stdlib import churn (adding sort/strings) is dropped
//     entirely.
func TestRubric_ImportChoices(t *testing.T) {
	if os.Getenv("MEAT_E2E") != "1" {
		t.Skip("set MEAT_E2E=1 to run the live-LLM rubric test")
	}
	ctx := context.Background()
	m, err := NewAnthropicFromEnv(ctx, "")
	if err != nil {
		t.Skipf("no LLM available: %v", err)
	}

	t.Run("meaningful choice kept, boring companion dropped", func(t *testing.T) {
		const diff = `diff --git a/token.go b/token.go
--- a/token.go
+++ b/token.go
@@ -1,15 +1,16 @@
 package auth
 
 import (
-	"math/rand"
+	"crypto/rand"
+	"encoding/hex"
 	"fmt"
 )
 
 func NewToken() string {
 	b := make([]byte, 16)
-	for i := range b {
-		b[i] = byte(rand.Intn(256))
-	}
-	return fmt.Sprintf("%x", b)
+	if _, err := rand.Read(b); err != nil {
+		panic(err)
+	}
+	return hex.EncodeToString(b)
 }
`
		res, err := Abridge(ctx, m, Request{UnifiedDiff: diff})
		if err != nil {
			t.Fatal(err)
		}
		// The meaningful swap must survive on BOTH sides: the removed
		// math/rand and the added crypto/rand reveal the choice.
		if !strings.Contains(res.SmartDiff, "math/rand") {
			t.Errorf("removal of math/rand was elided (loses the 'from' side of the swap):\n%s", res.SmartDiff)
		}
		if !strings.Contains(res.SmartDiff, "crypto/rand") {
			t.Errorf("meaningful import choice crypto/rand was elided:\n%s", res.SmartDiff)
		}
		// The companion import added only because the new path uses it is noise.
		if strings.Contains(res.SmartDiff, "encoding/hex") {
			t.Errorf("boring companion import encoding/hex was kept:\n%s", res.SmartDiff)
		}
		// The behavioral body change (the actual secure read) must remain.
		if !strings.Contains(res.SmartDiff, "rand.Read") {
			t.Errorf("behavioral body change (rand.Read) was lost:\n%s", res.SmartDiff)
		}
	})

	t.Run("boring stdlib churn dropped", func(t *testing.T) {
		const diff = `diff --git a/util.go b/util.go
--- a/util.go
+++ b/util.go
@@ -1,8 +1,10 @@
 package util
 
 import (
 	"fmt"
+	"sort"
+	"strings"
 )
 
 func Join(parts []string) string {
-	return fmt.Sprint(parts)
+	sort.Strings(parts)
+	return strings.Join(parts, ",")
 }
`
		res, err := Abridge(ctx, m, Request{UnifiedDiff: diff})
		if err != nil {
			t.Fatal(err)
		}
		// The import lines should be gone; the behavioral body change stays.
		if strings.Contains(res.SmartDiff, `"sort"`) || strings.Contains(res.SmartDiff, `"strings"`) {
			t.Errorf("boring stdlib import churn was kept:\n%s", res.SmartDiff)
		}
		// Both halves of the behavioral change must survive: the new sort and
		// the new join. (Require both, not either.)
		if !strings.Contains(res.SmartDiff, "sort.Strings") {
			t.Errorf("behavioral change sort.Strings was lost:\n%s", res.SmartDiff)
		}
		if !strings.Contains(res.SmartDiff, "strings.Join") {
			t.Errorf("behavioral change strings.Join was lost:\n%s", res.SmartDiff)
		}
	})
}
