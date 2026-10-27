package models

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
)

var (
	ErrInvalidToolCallDelta = errors.New("invalid streamed tool call")
	ErrIncompleteToolCall   = errors.New("streamed tool call is incomplete")
)

const maxStreamToolCalls = 128

// ToolCallAccumulator merges provider deltas by index. Call IDs and names may be
// sent only once, while function names and JSON arguments may arrive fragmented.
type ToolCallAccumulator struct {
	calls map[int]*ToolCall
}

func (a *ToolCallAccumulator) Add(delta ToolCallDelta) error {
	if delta.Index < 0 || delta.Index >= maxStreamToolCalls || len(delta.ID) > 256 || len(delta.Name) > 256 {
		return ErrInvalidToolCallDelta
	}
	if a.calls == nil {
		a.calls = make(map[int]*ToolCall)
	}
	call := a.calls[delta.Index]
	if call == nil {
		if len(a.calls) >= maxStreamToolCalls {
			return ErrInvalidToolCallDelta
		}
		call = &ToolCall{Index: delta.Index}
		a.calls[delta.Index] = call
	}
	if delta.ID != "" {
		if call.ID != "" && call.ID != delta.ID {
			return ErrInvalidToolCallDelta
		}
		call.ID = delta.ID
	}
	if len(call.Name)+len(delta.Name) > 256 || len(call.Arguments)+len(delta.Arguments) > maxInstanceBytes {
		return ErrInvalidToolCallDelta
	}
	call.Name += delta.Name
	call.Arguments = append(call.Arguments, delta.Arguments...)
	return nil
}

// Calls returns completed tool calls ordered by provider index and rejects any
// incomplete call or argument stream that is not valid JSON.
func (a *ToolCallAccumulator) Calls() ([]ToolCall, error) {
	if len(a.calls) == 0 {
		return nil, nil
	}
	indices := make([]int, 0, len(a.calls))
	for index := range a.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	calls := make([]ToolCall, 0, len(indices))
	for _, index := range indices {
		call := a.calls[index]
		if call.ID == "" || strings.TrimSpace(call.Name) == "" || !json.Valid(call.Arguments) {
			return nil, ErrIncompleteToolCall
		}
		calls = append(calls, *call)
	}
	return calls, nil
}
