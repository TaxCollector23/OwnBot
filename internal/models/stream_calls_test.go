package models

import (
	"errors"
	"testing"
)

func TestToolCallAccumulatorAssemblesByIndexAndSorts(t *testing.T) {
	var a ToolCallAccumulator
	fragments := []ToolCallDelta{
		{Index: 1, ID: "call_b", Name: "lo", Arguments: `{"y":`},
		{Index: 0, ID: "call_a", Name: "get_", Arguments: `{"x":`},
		{Index: 1, Name: "okup", Arguments: `2}`},
		{Index: 0, Name: "item", Arguments: `1}`},
	}
	for _, fragment := range fragments {
		if err := a.Add(fragment); err != nil {
			t.Fatal(err)
		}
	}
	calls, err := a.Calls()
	if err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 || calls[0].Index != 0 || calls[0].Name != "get_item" || calls[0].ID != "call_a" || string(calls[0].Arguments) != `{"x":1}` || calls[1].Index != 1 || calls[1].Name != "lookup" || string(calls[1].Arguments) != `{"y":2}` {
		t.Fatalf("assembled calls: %#v", calls)
	}
}

func TestToolCallAccumulatorRejectsConflictsAndIncompleteCalls(t *testing.T) {
	var a ToolCallAccumulator
	if err := a.Add(ToolCallDelta{Index: 0, ID: "one", Name: "f", Arguments: `{}`}); err != nil {
		t.Fatal(err)
	}
	if err := a.Add(ToolCallDelta{Index: 0, ID: "two"}); !errors.Is(err, ErrInvalidToolCallDelta) {
		t.Fatalf("conflicting ID: %v", err)
	}
	var incomplete ToolCallAccumulator
	_ = incomplete.Add(ToolCallDelta{Index: 0, ID: "missing-args", Name: "f", Arguments: `{"x":`})
	if _, err := incomplete.Calls(); !errors.Is(err, ErrIncompleteToolCall) {
		t.Fatalf("incomplete args: %v", err)
	}
}
