package meat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// DefaultAnthropicModel is the model used when AnthropicModel.Model is empty.
const DefaultAnthropicModel = "claude-sonnet-4-5"

// AnthropicModel is a built-in Model backed by the Anthropic Messages API. It
// uses only the standard library so meat.dev has no third-party dependencies.
type AnthropicModel struct {
	APIKey  string       // required
	Model   string       // defaults to DefaultAnthropicModel
	BaseURL string       // defaults to https://api.anthropic.com
	HTTPC   *http.Client // defaults to a client with a 2m timeout
}

// NewAnthropicFromEnv builds an AnthropicModel from ANTHROPIC_API_KEY,
// ANTHROPIC_BASE_URL, and the given model (falling back to $MEAT_MODEL then the
// default). It returns an error if no API key is configured.
func NewAnthropicFromEnv(model string) (*AnthropicModel, error) {
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY is not set")
	}
	if model == "" {
		model = os.Getenv("MEAT_MODEL")
	}
	return &AnthropicModel{
		APIKey:  key,
		Model:   model,
		BaseURL: os.Getenv("ANTHROPIC_BASE_URL"),
	}, nil
}

// --- wire types ---

type antReq struct {
	Model     string       `json:"model"`
	MaxTokens int          `json:"max_tokens"`
	System    string       `json:"system"`
	Messages  []antMessage `json:"messages"`
	Tools     []antTool    `json:"tools,omitempty"`
}

type antMessage struct {
	Role    string     `json:"role"`
	Content []antBlock `json:"content"`
}

type antBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   string          `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

type antTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

type antResp struct {
	Content []antBlock `json:"content"`
	Usage   struct {
		InputTokens  int `json:"input_tokens"`
		OutputTokens int `json:"output_tokens"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Generate implements Model.
func (m *AnthropicModel) Generate(ctx context.Context, system string, messages []Message, tools []Tool) (*Response, error) {
	if m.APIKey == "" {
		return nil, fmt.Errorf("meat: AnthropicModel.APIKey is empty")
	}

	reqBody := antReq{
		Model:     cmpOr(m.Model, DefaultAnthropicModel),
		MaxTokens: 8192,
		System:    system,
		Messages:  toAntMessages(messages),
		Tools:     toAntTools(tools),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	url := strings.TrimRight(cmpOr(m.BaseURL, "https://api.anthropic.com"), "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-api-key", m.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := m.HTTPC
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Minute}
	}
	httpResp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer httpResp.Body.Close()
	raw, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("anthropic API %d: %s", httpResp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var resp antResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("anthropic error: %s: %s", resp.Error.Type, resp.Error.Message)
	}

	out := &Response{
		InputTokens:  resp.Usage.InputTokens,
		OutputTokens: resp.Usage.OutputTokens,
	}
	for _, b := range resp.Content {
		switch b.Type {
		case "text":
			out.Content = append(out.Content, Block{Type: "text", Text: b.Text})
		case "tool_use":
			out.Content = append(out.Content, Block{Type: "tool_use", ID: b.ID, ToolName: b.Name, ToolInput: b.Input})
		}
	}
	return out, nil
}

func toAntTools(tools []Tool) []antTool {
	out := make([]antTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, antTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}

func toAntMessages(messages []Message) []antMessage {
	out := make([]antMessage, 0, len(messages))
	for _, m := range messages {
		am := antMessage{Role: string(m.Role)}
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				am.Content = append(am.Content, antBlock{Type: "text", Text: b.Text})
			case "tool_use":
				am.Content = append(am.Content, antBlock{Type: "tool_use", ID: b.ID, Name: b.ToolName, Input: b.ToolInput})
			case "tool_result":
				am.Content = append(am.Content, antBlock{Type: "tool_result", ToolUseID: b.ToolUseID, Content: b.ToolResult, IsError: b.ToolError})
			}
		}
		out = append(out, am)
	}
	return out
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
