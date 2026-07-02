package meat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// antServer runs an httptest stub of the messages endpoint whose behavior per
// attempt is scripted by handler, and returns a Model pointed at it.
func antServer(t *testing.T, handler http.HandlerFunc) (*AnthropicModel, *int32) {
	t.Helper()
	// Shrink the backoff so retry tests run fast.
	old := retryBaseDelay
	retryBaseDelay = time.Millisecond
	t.Cleanup(func() { retryBaseDelay = old })
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &AnthropicModel{
		APIKey:  "test-key",
		BaseURL: srv.URL,
		HTTPC:   srv.Client(),
	}, &calls
}

func okBody(stopReason string) []byte {
	b, _ := json.Marshal(map[string]any{
		"content":     []map[string]any{{"type": "text", "text": "hi"}},
		"stop_reason": stopReason,
		"usage":       map[string]int{"input_tokens": 10, "output_tokens": 5},
	})
	return b
}

// TestGenerate_RetriesTransient verifies a 429 and a 529 are retried and the
// eventual 200 is returned.
func TestGenerate_RetriesTransient(t *testing.T) {
	var m *AnthropicModel
	var calls *int32
	m, calls = antServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch atomic.LoadInt32(calls) {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"type":"rate_limit_error","message":"slow down"}}`))
		case 2:
			w.WriteHeader(529)
			w.Write([]byte(`{"error":{"type":"overloaded_error","message":"overloaded"}}`))
		default:
			w.Write(okBody("end_turn"))
		}
	})

	// Retries sleep 1s+2s; keep the test bounded but real.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := m.Generate(ctx, "sys", []Message{{Role: RoleUser, Content: []Block{textBlock("x")}}}, nil)
	if err != nil {
		t.Fatalf("Generate after transient errors: %v", err)
	}
	if got := atomic.LoadInt32(calls); got != 3 {
		t.Errorf("attempts = %d, want 3 (two retries then success)", got)
	}
	if len(resp.Content) != 1 || resp.Content[0].Text != "hi" {
		t.Errorf("unexpected content: %+v", resp.Content)
	}
}

// TestGenerate_NoRetryOnBadRequest: a 400 is the caller's fault; retrying can't
// help and must not happen.
func TestGenerate_NoRetryOnBadRequest(t *testing.T) {
	m, calls := antServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":{"type":"invalid_request_error","message":"too large"}}`))
	})
	_, err := m.Generate(context.Background(), "sys", []Message{{Role: RoleUser, Content: []Block{textBlock("x")}}}, nil)
	if err == nil {
		t.Fatal("want error on 400")
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 4xx)", got)
	}
}

// TestGenerate_GivesUpAfterMaxAttempts: persistent 5xx exhausts the retry
// budget and surfaces the last error.
func TestGenerate_GivesUpAfterMaxAttempts(t *testing.T) {
	m, calls := antServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`boom`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := m.Generate(ctx, "sys", []Message{{Role: RoleUser, Content: []Block{textBlock("x")}}}, nil)
	if err == nil {
		t.Fatal("want error after exhausting retries")
	}
	if got := atomic.LoadInt32(calls); got != maxAttempts {
		t.Errorf("attempts = %d, want %d", got, maxAttempts)
	}
}

// TestGenerate_MaxTokensStopIsAnError: a response truncated at max_tokens must
// error rather than silently return a cut-off (and cacheable) result.
func TestGenerate_MaxTokensStopIsAnError(t *testing.T) {
	m, _ := antServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write(okBody("max_tokens"))
	})
	_, err := m.Generate(context.Background(), "sys", []Message{{Role: RoleUser, Content: []Block{textBlock("x")}}}, nil)
	if err == nil {
		t.Fatal("want error when stop_reason is max_tokens")
	}
}

// TestGenerate_SendsMaxOutputTokens pins the request-side cap.
func TestGenerate_SendsMaxOutputTokens(t *testing.T) {
	var gotMax int
	m, _ := antServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			MaxTokens int `json:"max_tokens"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		gotMax = req.MaxTokens
		w.Write(okBody("end_turn"))
	})
	if _, err := m.Generate(context.Background(), "sys", []Message{{Role: RoleUser, Content: []Block{textBlock("x")}}}, nil); err != nil {
		t.Fatal(err)
	}
	if gotMax != maxOutputTokens {
		t.Errorf("max_tokens sent = %d, want %d", gotMax, maxOutputTokens)
	}
}
