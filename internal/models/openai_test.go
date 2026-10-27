package models

import (
	"context"
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
