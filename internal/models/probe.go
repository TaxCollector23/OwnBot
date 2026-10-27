package models

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ProbeResult records observed behaviors only; an error or omitted behavior scores zero.
type ProbeResult struct {
	Score            int  `json:"score"`
	JSONOutput       bool `json:"json_output"`
	SingleTool       bool `json:"single_tool"`
	ToolChoice       bool `json:"tool_choice"`
	ArgumentAccuracy bool `json:"argument_accuracy"`
	Restricted       bool `json:"restricted"`
}

// Probe performs four small calls to assess whether an endpoint is safe for tool use.
// threshold is inclusive and callers should persist this result alongside the model.
func Probe(ctx context.Context, c Client, model string, threshold int) (ProbeResult, error) {
	p := ProbeResult{Restricted: true}
	call := func(prompt string, tools []Tool, choice string, format json.RawMessage) (Response, error) {
		return c.Chat(ctx, Request{Model: model, Messages: []Message{{Role: "user", Content: prompt}}, Tools: tools, ToolChoice: choice, ResponseFormat: format, MaxTokens: 96})
	}
	fmtSchema := json.RawMessage(`{"type":"json_object"}`)
	r, e := call(`Return only JSON: {"ok":true}`, nil, "", fmtSchema)
	if e != nil {
		return p, e
	}
	var obj map[string]any
	p.JSONOutput = json.Unmarshal([]byte(r.Message.Content), &obj) == nil && obj["ok"] == true
	one := Tool{Name: "probe_one", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)}
	r, e = call(`Call probe_one with value exactly "alpha".`, []Tool{one}, "required", nil)
	if e != nil {
		return p, e
	}
	if len(r.Message.ToolCalls) == 1 && r.Message.ToolCalls[0].Name == one.Name {
		p.SingleTool = true
		var a map[string]any
		p.ArgumentAccuracy = json.Unmarshal(r.Message.ToolCalls[0].Arguments, &a) == nil && a["value"] == "alpha"
	}
	two := Tool{Name: "probe_two", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"]}`)}
	r, e = call(`Call probe_two with value exactly "beta".`, []Tool{one, two}, "required", nil)
	if e != nil {
		return p, e
	}
	p.ToolChoice = len(r.Message.ToolCalls) == 1 && r.Message.ToolCalls[0].Name == "probe_two"
	for _, ok := range []bool{p.JSONOutput, p.SingleTool, p.ToolChoice, p.ArgumentAccuracy} {
		if ok {
			p.Score += 25
		}
	}
	p.Restricted = p.Score < threshold
	return p, nil
}

// ValidateToolCallJSON checks call identity and JSON syntax only; it does not perform JSON Schema validation.
func ValidateToolCallJSON(call ToolCall, tool Tool) error {
	if call.Name != tool.Name {
		return fmt.Errorf("tool call %q does not match schema %q", call.Name, tool.Name)
	}
	if !json.Valid(call.Arguments) {
		return fmt.Errorf("tool %s returned invalid JSON arguments", call.Name)
	}
	var v any
	if err := json.Unmarshal(tool.Parameters, &v); err != nil {
		return fmt.Errorf("tool %s has invalid schema: %w", tool.Name, err)
	}
	if strings.TrimSpace(string(call.Arguments)) == "" {
		return fmt.Errorf("tool %s returned empty arguments", call.Name)
	}
	return nil
}
