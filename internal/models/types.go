package models

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Message uses the common chat vocabulary. Tool results are messages with role "tool".
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content,omitempty"`
	Name       string     `json:"name,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
}

type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters"`
}

type ToolCall struct {
	ID             string          `json:"id"`
	Name           string          `json:"name"`
	Arguments      json.RawMessage `json:"arguments,omitempty"`
	ArgumentsDelta string          `json:"arguments_delta,omitempty"`
}

type Request struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Tools          []Tool          `json:"tools,omitempty"`
	ToolChoice     string          `json:"tool_choice,omitempty"`
	ResponseFormat json.RawMessage `json:"response_format,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
}

type Response struct {
	ID      string    `json:"id,omitempty"`
	Model   string    `json:"model,omitempty"`
	Message Message   `json:"message"`
	Usage   Usage     `json:"usage,omitempty"`
	Created time.Time `json:"created,omitempty"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens,omitempty"`
	CompletionTokens int `json:"completion_tokens,omitempty"`
	TotalTokens      int `json:"total_tokens,omitempty"`
}

type StreamEvent struct {
	ID    string
	Delta Message
	Done  bool
	Usage *Usage
	Err   error
}

type Client interface {
	Chat(context.Context, Request) (Response, error)
	Stream(context.Context, Request) (<-chan StreamEvent, error)
}

var ErrUnsupported = errors.New("model provider does not support this operation")
