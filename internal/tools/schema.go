package tools

import (
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
)

// schemaTweaks adjusts the input schema the SDK infers from a Go struct.
// The jsonschema struct tag can only carry a description, yet a model
// client validates calls against the published schema, so enums, defaults
// and minimum list sizes have to be set here.
type schemaTweaks struct {
	// enums lists the allowed values of string properties by JSON name.
	enums map[string][]string
	// defaults holds the default value of properties by JSON name.
	defaults map[string]any
	// minItems is the minimum length of array properties by JSON name;
	// it also makes the property a plain "array" instead of
	// ["null", "array"] so null is rejected before the handler runs.
	minItems map[string]int
}

// inputSchema infers the schema of In and applies t. It panics on an
// inference error or an unknown property name: both are programming
// errors caught by the tests at registration time.
func inputSchema[In any](t schemaTweaks) *jsonschema.Schema {
	s, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tools: input schema: %v", err))
	}
	for name, values := range t.enums {
		p := property(s, name)
		p.Enum = make([]any, 0, len(values))
		for _, v := range values {
			p.Enum = append(p.Enum, v)
		}
	}
	for name, value := range t.defaults {
		raw, err := json.Marshal(value)
		if err != nil {
			panic(fmt.Sprintf("tools: default of %s: %v", name, err))
		}
		property(s, name).Default = raw
	}
	for name, n := range t.minItems {
		p := property(s, name)
		p.Types = nil
		p.Type = "array"
		p.MinItems = ptr(n)
	}
	return s
}

// property returns the schema of the named property or panics.
func property(s *jsonschema.Schema, name string) *jsonschema.Schema {
	p, ok := s.Properties[name]
	if !ok {
		panic(fmt.Sprintf("tools: schema has no property %q", name))
	}
	return p
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }

// planTweaks are the schema tweaks every plan-style input shares. Enums
// are deliberately not set: the parsers accept any casing ("krw",
// "Daily") and give a better message than a schema failure would.
func planTweaks(extra map[string]int) schemaTweaks {
	return schemaTweaks{
		defaults: map[string]any{"currency": "USD", "cadence": "daily", "fee_rate": 0, "commission_fixed": 0, "reinvest_dividends": true, "compare_with": defaultBaseline},
		minItems: extra,
	}
}
