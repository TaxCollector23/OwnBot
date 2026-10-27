package models

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestChatNormalizesToolArgumentsAndDoesNotExposeErrorBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("missing auth")
		}
		fmt.Fprint(w, `{"id":"x","model":"m","choices":[{"message":{"role":"assistant","content":"<think>hidden</think>ok","tool_calls":[{"id":"c","function":{"name":"f","arguments":"{\"v\":1}"}}]}}]}`)
	}))
	defer server.Close()
	got, err := (&OpenAICompatible{BaseURL: server.URL, APIKey: "secret"}).Chat(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Message.Content != "ok" || len(got.Message.ToolCalls) != 1 || string(got.Message.ToolCalls[0].Arguments) != `{"v":1}` {
		t.Fatalf("unexpected response: %#v", got)
	}
}

func TestChatProviderErrorDoesNotLeakBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		fmt.Fprint(w, "secret-token-provider-detail")
	}))
	defer server.Close()
	_, err := (&OpenAICompatible{BaseURL: server.URL}).Chat(context.Background(), Request{})
	if err == nil || strings.Contains(err.Error(), "secret-token-provider-detail") {
		t.Fatalf("error leaked or missing: %v", err)
	}
}

func TestStreamFiltersThinkTagsAcrossChunks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi <thi\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"nk>secret</think> there\"}}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	ch, err := (&OpenAICompatible{BaseURL: server.URL}).Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var got strings.Builder
	done := false
	for ev := range ch {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
		got.WriteString(ev.Delta.Content)
		done = done || ev.Done
	}
	if got.String() != "hi  there" || !done {
		t.Fatalf("content=%q done=%v", got.String(), done)
	}
}

func TestStreamAssemblesToolCallFragmentsAndFinishReason(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_1\",\"function\":{\"name\":\"get_\",\"arguments\":\"{\\\"x\\\":\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"name\":\"item\",\"arguments\":\"1}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	ch, err := (&OpenAICompatible{BaseURL: server.URL}).Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var accumulator ToolCallAccumulator
	var finish string
	done := false
	for ev := range ch {
		if ev.Err != nil {
			t.Fatal(ev.Err)
		}
		if ev.FinishReason != "" {
			finish = ev.FinishReason
		}
		done = done || ev.Done
		for _, delta := range ev.ToolCallDeltas {
			if err := accumulator.Add(delta); err != nil {
				t.Fatal(err)
			}
		}
	}
	calls, err := accumulator.Calls()
	if err != nil {
		t.Fatal(err)
	}
	if !done || finish != "tool_calls" || len(calls) != 1 || calls[0].ID != "call_1" || calls[0].Name != "get_item" || string(calls[0].Arguments) != `{"x":1}` {
		t.Fatalf("done=%v finish=%q calls=%+v", done, finish, calls)
	}
}

func TestStreamEOFWithoutSentinelIsTerminalErrorAndHidesOpenThink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"visible <think>secret\"}}]}\n\n")
	}))
	defer server.Close()
	ch, err := (&OpenAICompatible{BaseURL: server.URL}).Stream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	var visible strings.Builder
	var terminal error
	for ev := range ch {
		visible.WriteString(ev.Delta.Content)
		if ev.Err != nil {
			terminal = ev.Err
		}
	}
	if visible.String() != "visible " {
		t.Fatalf("thinking text leaked: %q", visible.String())
	}
	if !errors.Is(terminal, ErrTruncatedStream) {
		t.Fatalf("terminal error = %v", terminal)
	}
}
