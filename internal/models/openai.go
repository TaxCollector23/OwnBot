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

func toWire(r Request) map[string]any {
	m := map[string]any{"model": r.Model, "messages": r.Messages, "stream": false}
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
		var v any
		if json.Unmarshal(r.ResponseFormat, &v) == nil {
			m["response_format"] = v
		}
	}
	if len(r.Tools) > 0 {
		tools := make([]any, 0, len(r.Tools))
		for _, t := range r.Tools {
			var schema any
			_ = json.Unmarshal(t.Parameters, &schema)
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": schema}})
		}
		m["tools"] = tools
	}
	return m
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
	body, err := json.Marshal(toWire(r))
	if err != nil {
		return Response{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Response{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("model request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Response{}, fmt.Errorf("model provider returned HTTP %d", resp.StatusCode)
	}
	var wr wireResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&wr); err != nil {
		return Response{}, fmt.Errorf("decode model response: %w", err)
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
				Index    int    `json:"index"`
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

func (c *OpenAICompatible) Stream(ctx context.Context, r Request) (<-chan StreamEvent, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	payload := toWire(r)
	payload["stream"] = true
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("model stream request failed: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, fmt.Errorf("model provider returned HTTP %d", resp.StatusCode)
	}
	out := make(chan StreamEvent)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 4096), 1<<20)
		var data []string
		filter := thinkFilter{}
		flush := func() bool {
			if len(data) == 0 {
				return true
			}
			raw := strings.Join(data, "\n")
			data = nil
			if raw == "[DONE]" {
				select {
				case out <- StreamEvent{Done: true}:
					return true
				case <-ctx.Done():
					return false
				}
			}
			var e streamLine
			if json.Unmarshal([]byte(raw), &e) != nil {
				return true
			}
			ev := StreamEvent{ID: e.ID}
			if e.Usage != nil {
				ev.Usage = e.Usage
			}
			if len(e.Choices) > 0 {
				d := e.Choices[0].Delta
				ev.Delta = Message{Role: d.Role, Content: filter.feed(d.Content)}
				for _, tc := range d.ToolCalls {
					ev.Delta.ToolCalls = append(ev.Delta.ToolCalls, ToolCall{ID: tc.ID, Name: tc.Function.Name, ArgumentsDelta: tc.Function.Arguments})
				}
				ev.Done = e.Choices[0].FinishReason != nil
			}
			select {
			case out <- ev:
				return true
			case <-ctx.Done():
				return false
			}
		}
		for sc.Scan() {
			line := strings.TrimSuffix(sc.Text(), "\r")
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
		if err := sc.Err(); err != nil {
			select {
			case out <- StreamEvent{Err: err}:
			case <-ctx.Done():
			}
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
