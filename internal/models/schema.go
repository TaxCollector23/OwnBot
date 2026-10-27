package models

import (
	"bytes"
	"encoding/json"
	"errors"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

var (
	ErrInvalidJSONSchema = errors.New("invalid JSON Schema")
	ErrSchemaMismatch    = errors.New("JSON value does not satisfy JSON Schema")
	ErrToolNameMismatch  = errors.New("tool call does not match the selected tool")
)

const (
	maxSchemaBytes   = 1 << 20
	maxInstanceBytes = 1 << 20
)

// ValidateJSONSchema compiles a JSON Schema and validates one JSON value against
// it. External $ref loading is disabled so untrusted schemas cannot cause file or
// network access. Validation errors intentionally omit instance values.
func ValidateJSONSchema(schema, instance json.RawMessage) error {
	if len(instance) == 0 || len(instance) > maxInstanceBytes || !json.Valid(instance) {
		return ErrSchemaMismatch
	}
	compiled, err := compileJSONSchema(schema)
	if err != nil {
		return err
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(instance))
	if err != nil {
		return ErrSchemaMismatch
	}
	if err := compiled.Validate(value); err != nil {
		return ErrSchemaMismatch
	}
	return nil
}

func compileJSONSchema(schema json.RawMessage) (*jsonschema.Schema, error) {
	if len(schema) == 0 || len(schema) > maxSchemaBytes || !json.Valid(schema) {
		return nil, ErrInvalidJSONSchema
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, ErrInvalidJSONSchema
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(denyExternalSchemaLoader{})
	const schemaURL = "https://ownbot.invalid/schema.json"
	if err := compiler.AddResource(schemaURL, doc); err != nil {
		return nil, ErrInvalidJSONSchema
	}
	compiled, err := compiler.Compile(schemaURL)
	if err != nil {
		return nil, ErrInvalidJSONSchema
	}
	return compiled, nil
}

type denyExternalSchemaLoader struct{}

func (denyExternalSchemaLoader) Load(string) (any, error) {
	return nil, errors.New("external JSON Schema references are disabled")
}
