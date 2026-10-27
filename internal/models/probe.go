package models

import (
	"context"
	"encoding/json"
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
	jsonSchema := json.RawMessage(`{"type":"object","properties":{"ok":{"type":"boolean"}},"required":["ok"],"additionalProperties":false}`)
	p.JSONOutput = ValidateJSONSchema(jsonSchema, json.RawMessage(r.Message.Content)) == nil
	if p.JSONOutput {
		var obj struct {
			OK bool `json:"ok"`
		}
		p.JSONOutput = json.Unmarshal([]byte(r.Message.Content), &obj) == nil && obj.OK
	}
	one := Tool{Name: "probe_one", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)}
	r, e = call(`Call probe_one with value exactly "alpha".`, []Tool{one}, "required", nil)
	if e != nil {
		return p, e
	}
	p.SingleTool = len(r.Message.ToolCalls) == 1
	argumentOne := false
	if p.SingleTool && r.Message.ToolCalls[0].Name == one.Name {
		argumentOne = ValidateToolCallJSON(r.Message.ToolCalls[0], one) == nil
		if argumentOne {
			var a struct {
				Value string `json:"value"`
			}
			argumentOne = json.Unmarshal(r.Message.ToolCalls[0].Arguments, &a) == nil && a.Value == "alpha"
		}
	}
	p.ArgumentAccuracy = argumentOne
	two := Tool{Name: "probe_two", Parameters: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}},"required":["value"],"additionalProperties":false}`)}
	r, e = call(`Call probe_two with value exactly "beta".`, []Tool{one, two}, "required", nil)
	if e != nil {
		return p, e
	}
	p.ToolChoice = len(r.Message.ToolCalls) == 1 && r.Message.ToolCalls[0].Name == two.Name
	argumentTwo := false
	if p.ToolChoice && ValidateToolCallJSON(r.Message.ToolCalls[0], two) == nil {
		var a struct {
			Value string `json:"value"`
		}
		argumentTwo = json.Unmarshal(r.Message.ToolCalls[0].Arguments, &a) == nil && a.Value == "beta"
	}
	p.ArgumentAccuracy = p.ArgumentAccuracy && argumentTwo
	for _, ok := range []bool{p.JSONOutput, p.SingleTool, p.ToolChoice, p.ArgumentAccuracy} {
		if ok {
			p.Score += 25
		}
	}
	p.Restricted = p.Score < threshold
	return p, nil
}

func ValidateToolCallJSON(call ToolCall, tool Tool) error {
	if call.Name != tool.Name {
		return ErrToolNameMismatch
	}
	return ValidateJSONSchema(tool.Parameters, call.Arguments)
}
