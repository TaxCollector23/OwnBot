package models

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidateJSONSchemaRejectsExtraAndMistypedFields(t *testing.T) {
	schema := json.RawMessage(`{"type":"object","properties":{"title":{"type":"string"},"count":{"type":"integer"}},"required":["title","count"],"additionalProperties":false}`)
	for _, tc := range []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "valid", value: `{"title":"x","count":2}`},
		{name: "extra field", value: `{"title":"x","count":2,"extra":true}`, wantErr: true},
		{name: "wrong type", value: `{"title":"x","count":"2"}`, wantErr: true},
		{name: "missing required", value: `{"title":"x"}`, wantErr: true},
		{name: "not JSON", value: `{"title":`, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateJSONSchema(schema, json.RawMessage(tc.value))
			if tc.wantErr && err == nil {
				t.Fatal("expected validation error")
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidateJSONSchemaRejectsBadAndRemoteSchemas(t *testing.T) {
	if err := ValidateJSONSchema(json.RawMessage(`{`), json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidJSONSchema) {
		t.Fatalf("bad schema: %v", err)
	}
	if err := ValidateJSONSchema(json.RawMessage(`{"$ref":"https://example.invalid/schema.json"}`), json.RawMessage(`{}`)); !errors.Is(err, ErrInvalidJSONSchema) {
		t.Fatalf("external ref must be disabled: %v", err)
	}
}

func TestValidateToolCallJSONChecksNameAndSchema(t *testing.T) {
	tool := Tool{Name: "lookup", Parameters: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`)}
	if err := ValidateToolCallJSON(ToolCall{Name: "lookup", Arguments: json.RawMessage(`{"id":"a"}`)}, tool); err != nil {
		t.Fatal(err)
	}
	if err := ValidateToolCallJSON(ToolCall{Name: "other", Arguments: json.RawMessage(`{"id":"a"}`)}, tool); !errors.Is(err, ErrToolNameMismatch) {
		t.Fatalf("name mismatch: %v", err)
	}
	if err := ValidateToolCallJSON(ToolCall{Name: "lookup", Arguments: json.RawMessage(`{"id":"a","extra":true}`)}, tool); !errors.Is(err, ErrSchemaMismatch) {
		t.Fatalf("schema mismatch: %v", err)
	}
}
