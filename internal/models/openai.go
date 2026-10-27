package models

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAICompatible talks to /chat/completions on OpenAI-compatible servers such as
// Ollama, llama.cpp, vLLM, LM Studio, and hosted providers. BaseURL is the API root
// (for example http://127.0.0.1:11434/v1); credentials are never included in errors.
type OpenAICompatible struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func (c *OpenAICompatible) httpClient() *http.Client {
	if c.Client != nil {
		return c.Client
	}
	return &http.Client{Timeout: 5 * time.Minute}
}

func (c *OpenAICompatible) validate() error {
	if strings.TrimSpace(c.BaseURL) == "" {
		return errors.New("model base URL is required")
	}
	return nil
}

type wireMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	Name       string         `json:"name,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	ToolCalls  []wireToolCall `json:"tool_calls,omitempty"`
}
type wireToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function wireFunction `json:"function"`
}
type wireFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func toWire(r Request, stream bool) (map[string]any, error) {
	messages := make([]wireMessage, 0, len(r.Messages))
	for _, msg := range r.Messages {
		wm := wireMessage{Role: msg.Role, Content: msg.Content, Name: msg.Name, ToolCallID: msg.ToolCallID}
		for _, tc := range msg.ToolCalls {
			if tc.Name == "" || tc.ID == "" || !json.Valid(tc.Arguments) || len(tc.Arguments) == 0 {
				return nil, errors.New("request contains an incomplete tool call")
			}
			wm.ToolCalls = append(wm.ToolCalls, wireToolCall{ID: tc.ID, Type: "function", Function: wireFunction{Name: tc.Name, Arguments: string(tc.Arguments)}})
		}
		messages = append(messages, wm)
	}
	m := map[string]any{"model": r.Model, "messages": messages, "stream": stream}
	if r.Temperature != nil {
		m["temperature"] = *r.Temperature
	}
	if r.MaxTokens > 0 {
		m["max_tokens"] = r.MaxTokens
	}
	if r.ToolChoice != "" {
		m["tool_choice"] = r.ToolChoice
	}
	if len(r.ResponseFormat) != 0 {
		if !json.Valid(r.ResponseFormat) {
			return nil, errors.New("request contains an invalid response format")
		}
		var responseFormat map[string]any
		if json.Unmarshal(r.ResponseFormat, &responseFormat) != nil || responseFormat == nil {
			return nil, errors.New("response format must be a JSON object")
		}
		m["response_format"] = responseFormat
	}
	if len(r.Tools) > 0 {
		tools := make([]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			if t.Name == "" {
				return nil, errors.New("request contains a tool without a name")
			}
			if _, err := compileJSONSchema(t.Parameters); err != nil {
				return nil, errors.New("request contains an invalid tool schema")
			}
			var schema any
			if err := json.Unmarshal(t.Parameters, &schema); err != nil {
				return nil, errors.New("request contains an invalid tool schema")
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": schema}})
		}
		m["tools"] = tools
	}
	return m, nil
}

type wireResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string          `json:"name"`
					Arguments json.RawMessage `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
}

func (c *OpenAICompatible) Chat(ctx context.Context, r Request) (Response, error) {
	if err := c.validate(); err != nil {
		return Response{}, err
	}
	payload, err := toWire(r, false)
	if err != nil {
		return Response{}, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return Response{}, errors.New("encode model request failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, errors.New("build model request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Response{}, errors.New("model request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("model provider returned HTTP %d", resp.StatusCode)
	}
	var wr wireResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&wr); err != nil {
		return Response{}, errors.New("decode model response failed")
	}
	if len(wr.Choices) == 0 {
		return Response{}, errors.New("model provider returned no choices")
	}
	ch := wr.Choices[0].Message
	m := Message{Role: ch.Role, Content: stripThink(ch.Content)}
	for _, tc := range ch.ToolCalls {
		m.ToolCalls = append(m.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: normalizeArguments(tc.Function.Arguments)})
	}
	return Response{ID: wr.ID, Model: wr.Model, Message: m, Usage: wr.Usage}, nil
}

func normalizeArguments(raw json.RawMessage) json.RawMessage {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		return json.RawMessage(encoded)
	}
	return raw
}

// stripThink discards complete <think> sections and any unclosed thinking tail.
func stripThink(s string) string {
	for {
		a := strings.Index(s, "<think>")
		if a < 0 {
			return s
		}
		b := strings.Index(s[a+7:], "</think>")
		if b < 0 {
			return strings.TrimSpace(s[:a])
		}
		s = s[:a] + s[a+7+b+8:]
	}
}

type streamLine struct {
	ID      string `json:"id"`
	Choices []struct {
		Delta struct {
			Role      string `json:"role"`
			Content   string `json:"content"`
			ToolCalls []struct {
				Index    *int   `json:"index"`
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage *Usage `json:"usage"`
}

var ErrTruncatedStream = errors.New("model stream ended before the completion sentinel")

func (c *OpenAICompatible) Stream(ctx context.Context, r Request) (<-chan StreamEvent, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	payload, err := toWire(r, true)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("encode model stream request failed")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("build model stream request failed")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, errors.New("model stream request failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("model provider returned HTTP %d", resp.StatusCode)
	}
	out := make(chan StreamEvent)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		var data []string
		filter := thinkFilter{}
		terminal := false
		emit := func(ev StreamEvent) bool {
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		flush := func() bool {
			if len(data) == 0 {
				return true
			}
			raw := strings.Join(data, "\n")
			data = nil
			if raw == "[DONE]" {
				terminal = true
				_ = emit(StreamEvent{Done: true})
				return false
			}
			var parsed streamLine
			if json.Unmarshal([]byte(raw), &parsed) != nil {
				_ = emit(StreamEvent{Err: errors.New("model provider returned malformed SSE data")})
				return false
			}
			ev := StreamEvent{ID: parsed.ID}
			if parsed.Usage != nil {
				ev.Usage = parsed.Usage
			}
			if len(parsed.Choices) > 0 {
				choice := parsed.Choices[0]
				delta := choice.Delta
				ev.Delta = Message{Role: delta.Role, Content: filter.feed(delta.Content)}
				if choice.FinishReason != nil {
					ev.FinishReason = *choice.FinishReason
				}
				for _, tc := range delta.ToolCalls {
					if tc.Index == nil {
						_ = emit(StreamEvent{Err: errors.New("model provider omitted streamed tool call index")})
						return false
					}
					callDelta := ToolCallDelta{Index: *tc.Index, ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}
					ev.ToolCallDeltas = append(ev.ToolCallDeltas, callDelta)
					ev.Delta.ToolCalls = append(ev.Delta.ToolCalls, ToolCall{Index: *tc.Index, ID: tc.ID, Name: tc.Function.Name, ArgumentsDelta: tc.Function.Arguments})
				}
			}
			return emit(ev)
		}
		for scanner.Scan() {
			line := strings.TrimSuffix(scanner.Text(), "\r")
			if line == "" {
				if !flush() {
					return
				}
				continue
			}
			if strings.HasPrefix(line, "data:") {
				data = append(data, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			}
		}
		if ctx.Err() != nil {
			return
		}
		if err := scanner.Err(); err != nil {
			_ = emit(StreamEvent{Err: errors.New("read model stream failed")})
			return
		}
		if len(data) != 0 {
			_ = emit(StreamEvent{Err: ErrTruncatedStream})
			return
		}
		if !terminal {
			_ = emit(StreamEvent{Err: ErrTruncatedStream})
		}
	}()
	return out, nil
}

// thinkFilter removes <think> sections even when a tag is split across SSE chunks.
type thinkFilter struct {
	inside  bool
	pending string
}

func (f *thinkFilter) feed(chunk string) string {
	s := f.pending + chunk
	f.pending = ""
	var out strings.Builder
	for len(s) > 0 {
		tag := "<think>"
		if f.inside {
			tag = "</think>"
		}
		if i := strings.Index(s, tag); i >= 0 {
			if !f.inside {
				out.WriteString(s[:i])
			}
			s = s[i+len(tag):]
			f.inside = !f.inside
			continue
		}
		keep := 0
		for n := 1; n < len(tag) && n <= len(s); n++ {
			if strings.HasSuffix(s, tag[:n]) {
				keep = n
			}
		}
		ready := s[:len(s)-keep]
		if !f.inside {
			out.WriteString(ready)
		}
		f.pending = s[len(s)-keep:]
		s = ""
	}
	return out.String()
}
