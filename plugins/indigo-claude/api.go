package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const (
	anthropicAPIURL = "https://api.anthropic.com/v1/messages"
	defaultModel    = "claude-opus-4-8"
	maxTokens       = 8096
)

// modelAliases maps short names to full Anthropic model IDs. In API mode
// user input is resolved through this table before the request is sent; in
// CLI mode the raw alias is passed straight to the claude CLI, which
// resolves these (and more) itself.
var modelAliases = map[string]string{
	"opus":   "claude-opus-4-8",
	"sonnet": "claude-sonnet-5",
	"haiku":  "claude-haiku-4-5-20251001",
	"fable":  "claude-fable-5",
}

// modelAliasOrder lists aliases in display order for the /model help text.
var modelAliasOrder = []string{"opus", "sonnet", "haiku", "fable"}

// resolveModel returns the full model ID for API mode: an alias lookup, or
// the input unchanged when it isn't a known alias (so a full ID typed
// directly still works), or defaultModel when input is empty.
func resolveModel(input string) string {
	if input == "" {
		return defaultModel
	}
	if full, ok := modelAliases[input]; ok {
		return full
	}
	return input
}

// ─── message types ───────────────────────────────────────────────────────────

type apiMessage struct {
	Role    string     `json:"role"`
	Content []apiBlock `json:"content"`
}

func userText(text string) apiMessage {
	return apiMessage{Role: "user", Content: []apiBlock{textBlock(text)}}
}

// apiBlock is one content block — text, tool_use, or tool_result.
type apiBlock struct {
	Type string `json:"type"`

	// type=text
	Text string `json:"text,omitempty"`

	// type=tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// type=tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

func textBlock(text string) apiBlock { return apiBlock{Type: "text", Text: text} }
func toolUseBlock(id, name string, input json.RawMessage) apiBlock {
	return apiBlock{Type: "tool_use", ID: id, Name: name, Input: input}
}
func toolResultBlock(toolUseID, result string, isError bool) apiBlock {
	return apiBlock{Type: "tool_result", ToolUseID: toolUseID, Content: result, IsError: isError}
}

// toolDef describes a tool to the model.
type toolDef struct {
	Name        string     `json:"name"`
	Description string     `json:"description"`
	InputSchema toolSchema `json:"input_schema"`
}

type toolSchema struct {
	Type       string                `json:"type"`
	Properties map[string]schemaProp `json:"properties"`
	Required   []string              `json:"required,omitempty"`
}

type schemaProp struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// ─── streaming events ────────────────────────────────────────────────────────

type streamTextEvent struct{ text string }
type streamToolEvent struct {
	id    string
	name  string
	input json.RawMessage
}
type streamStopEvent struct{ stopReason string }

// streamUsageEvent carries token counts for the request: input side from
// message_start, output side from the final message_delta.
type streamUsageEvent struct{ ctxTokens int }

// ─── streaming API call ──────────────────────────────────────────────────────

func streamAPI(ctx context.Context, apiKey, model, system string, messages []apiMessage, tools []toolDef, onEvent func(any)) error {
	body := map[string]any{
		"model":      resolveModel(model),
		"max_tokens": maxTokens,
		"system":     system,
		"messages":   messages,
		"stream":     true,
	}
	if len(tools) > 0 {
		body["tools"] = tools
	}
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, anthropicAPIURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		var errBody map[string]any
		json.NewDecoder(resp.Body).Decode(&errBody) //nolint:errcheck
		if errObj, ok := errBody["error"].(map[string]any); ok {
			return fmt.Errorf("API %d: %v", resp.StatusCode, errObj["message"])
		}
		return fmt.Errorf("API error %d", resp.StatusCode)
	}

	return parseSSE(resp.Body, onEvent)
}

func parseSSE(body interface{ Read([]byte) (int, error) }, onEvent func(any)) error {
	type blockState struct {
		bType    string
		toolID   string
		toolName string
		inputBuf strings.Builder
	}
	blocks := map[int]*blockState{}
	inputTokens := 0 // input-side tokens captured from message_start

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 512*1024), 512*1024)

	var dataLine string
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			dataLine = strings.TrimPrefix(line, "data: ")
			continue
		}
		if line != "" || dataLine == "" {
			continue
		}
		data := dataLine
		dataLine = ""

		var ev map[string]json.RawMessage
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue
		}
		var evType string
		json.Unmarshal(ev["type"], &evType) //nolint:errcheck

		switch evType {
		case "message_start":
			var start struct {
				Message struct {
					Usage struct {
						InputTokens              int `json:"input_tokens"`
						CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
						CacheReadInputTokens     int `json:"cache_read_input_tokens"`
					} `json:"usage"`
				} `json:"message"`
			}
			if json.Unmarshal([]byte(data), &start) == nil {
				u := start.Message.Usage
				inputTokens = u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
			}

		case "content_block_start":
			var idx int
			json.Unmarshal(ev["index"], &idx) //nolint:errcheck
			var cb struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			json.Unmarshal(ev["content_block"], &cb) //nolint:errcheck
			blocks[idx] = &blockState{bType: cb.Type, toolID: cb.ID, toolName: cb.Name}

		case "content_block_delta":
			var idx int
			json.Unmarshal(ev["index"], &idx) //nolint:errcheck
			blk := blocks[idx]
			if blk == nil {
				continue
			}
			var delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			}
			json.Unmarshal(ev["delta"], &delta) //nolint:errcheck
			switch delta.Type {
			case "text_delta":
				onEvent(streamTextEvent{text: delta.Text})
			case "input_json_delta":
				blk.inputBuf.WriteString(delta.PartialJSON)
			}

		case "content_block_stop":
			var idx int
			json.Unmarshal(ev["index"], &idx) //nolint:errcheck
			blk := blocks[idx]
			if blk == nil {
				continue
			}
			if blk.bType == "tool_use" {
				raw := json.RawMessage(blk.inputBuf.String())
				if len(raw) == 0 {
					raw = json.RawMessage("{}")
				}
				onEvent(streamToolEvent{id: blk.toolID, name: blk.toolName, input: raw})
			}
			delete(blocks, idx)

		case "message_delta":
			var delta struct {
				Delta struct {
					StopReason string `json:"stop_reason"`
				} `json:"delta"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			json.Unmarshal([]byte(data), &delta) //nolint:errcheck
			if total := inputTokens + delta.Usage.OutputTokens; total > 0 {
				onEvent(streamUsageEvent{ctxTokens: total})
			}
			onEvent(streamStopEvent{stopReason: delta.Delta.StopReason})
		}
	}
	return scanner.Err()
}
