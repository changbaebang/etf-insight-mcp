package tools

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// explainArgumentErrors is a receiving middleware that rewrites the SDK's
// argument-validation errors into messages a model can act on. The SDK
// reports, for example,
//
//	validating "arguments": validating root: validating /properties/seed: minimum: -3/1 is less than 0.000000
//
// which becomes "invalid arguments: seed must be at least 0". Messages it
// does not recognise are left as they are.
func explainArgumentErrors(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		res, err := next(ctx, method, req)
		call, ok := req.(*mcp.CallToolRequest)
		if err != nil || !ok || call.Params == nil {
			return res, err
		}
		out, ok := res.(*mcp.CallToolResult)
		if !ok || out == nil || !out.IsError || len(out.Content) != 1 {
			return res, err
		}
		text, ok := out.Content[0].(*mcp.TextContent)
		if !ok {
			return res, err
		}
		if friendly, ok := friendlyArgumentError(call.Params.Name, text.Text); ok {
			text.Text = friendly
		}
		return res, err
	}
}

var (
	// argumentPathRE matches one "validating /properties/...: " step of a
	// schema error; the last one names the offending input.
	argumentPathRE = regexp.MustCompile(`validating (/[^:]*): `)
	// decodeErrorRE matches encoding/json's message for a value that fits
	// the schema but not the Go field, e.g. an integer too large for int.
	decodeErrorRE = regexp.MustCompile(`cannot unmarshal (\w+) (\S+) into Go struct field \w+\.([\w.]+) of type (\w+)`)
	// quotedRE matches the quoted names in a "missing properties" list.
	quotedRE = regexp.MustCompile(`"([^"]+)"`)
)

// friendlyArgumentError turns one SDK argument error into a short message
// naming the input and what is wrong with it. tool is the tool's name,
// used for hints such as pointing simulate_dca at simulate_portfolio_dca.
// ok is false when msg is not an argument error this function knows.
func friendlyArgumentError(tool, msg string) (string, bool) {
	if m := decodeErrorRE.FindStringSubmatch(msg); m != nil {
		return fmt.Sprintf("invalid arguments: %s must be %s, got %s %s", m[3], describeGoType(m[4]), m[1], m[2]), true
	}
	const prefix = `validating "arguments": validating root: `
	if !strings.HasPrefix(msg, prefix) {
		return "", false
	}
	rest := strings.TrimPrefix(msg, prefix)
	field := ""
	if steps := argumentPathRE.FindAllStringSubmatchIndex(rest, -1); len(steps) > 0 {
		last := steps[len(steps)-1]
		field = fieldName(rest[last[2]:last[3]])
		rest = rest[last[1]:]
	}
	rule, detail, found := strings.Cut(rest, ": ")
	if !found {
		rule, detail = "", rest
	}
	var friendly string
	switch {
	case rule == "required":
		missing := quotedNames(detail)
		if field == "" {
			friendly = "missing required input: " + strings.Join(missing, ", ")
		} else {
			friendly = fmt.Sprintf("each item of %s needs %s", strings.TrimSuffix(field, "[]"), strings.Join(missing, ", "))
		}
	case strings.HasPrefix(rest, "unexpected additional properties"):
		unknown := quotedNames(rest)
		friendly = fmt.Sprintf("%s has no input named %s", tool, strings.Join(unknown, ", "))
		friendly += wrongToolHint(tool, unknown)
	case rule == "type":
		friendly = describeTypeError(field, detail)
	case rule == "minItems":
		friendly = fmt.Sprintf("%s must list at least %s item(s)", field, lastWord(detail))
	case rule == "maxItems":
		friendly = fmt.Sprintf("%s may list at most %s item(s)", field, lastWord(detail))
	case rule == "minimum":
		friendly = fmt.Sprintf("%s must be at least %s", field, plainNumber(lastWord(detail)))
	case rule == "maximum":
		friendly = fmt.Sprintf("%s must be at most %s", field, plainNumber(lastWord(detail)))
	default:
		return "", false
	}
	return "invalid arguments: " + friendly, true
}

// fieldName turns a schema path such as /properties/allocations/items into
// an input name such as allocations[].
func fieldName(path string) string {
	path = strings.ReplaceAll(path, "/items", "[]")
	path = strings.ReplaceAll(path, "/properties/", ".")
	return strings.TrimPrefix(path, ".")
}

// quotedNames returns the names quoted in s, in order.
func quotedNames(s string) []string {
	var out []string
	for _, m := range quotedRE.FindAllStringSubmatch(s, -1) {
		out = append(out, m[1])
	}
	return out
}

// wrongToolHint points a single-ETF call with a portfolio input, or the
// reverse, at the right tool.
func wrongToolHint(tool string, unknown []string) string {
	has := func(name string) bool {
		for _, u := range unknown {
			if u == name {
				return true
			}
		}
		return false
	}
	switch {
	case tool == "simulate_dca" && has("allocations"):
		return "; for several ETFs use simulate_portfolio_dca"
	case tool == "simulate_portfolio_dca" && has("symbol"):
		return "; for one ETF use simulate_dca, or give allocations with a single entry"
	default:
		return "; check the input names in the tool's schema"
	}
}

// describeTypeError rewrites `5 has type "integer", want "string"`.
func describeTypeError(field, detail string) string {
	value, wantPart, ok := strings.Cut(detail, ` has type "`)
	if !ok {
		return fmt.Sprintf("%s has the wrong type (%s)", field, detail)
	}
	got, _, _ := strings.Cut(wantPart, `"`)
	want := quotedNames(wantPart[len(got)+1:])
	kinds := make([]string, 0, len(want))
	for _, w := range want {
		for _, k := range strings.Split(w, ", ") {
			if k != "null" {
				kinds = append(kinds, describeJSONType(k))
			}
		}
	}
	return fmt.Sprintf("%s must be %s, got the %s %s", field, strings.Join(kinds, " or "), got, value)
}

// describeJSONType names a JSON schema type in plain words.
func describeJSONType(t string) string {
	switch t {
	case "number":
		return "a number"
	case "integer":
		return "a whole number"
	case "string":
		return "a string"
	case "boolean":
		return "true or false"
	case "array":
		return "a list"
	case "object":
		return "an object"
	default:
		return t
	}
}

// describeGoType names the Go type a JSON value could not be decoded into.
func describeGoType(t string) string {
	switch t {
	case "int", "int64", "int32":
		return "a whole number within the 64-bit range"
	case "uint64", "uint", "uint32":
		return "a whole number from 0 to 9007199254740991"
	case "float64":
		return "a number"
	case "string":
		return "a string"
	case "bool":
		return "true or false"
	default:
		return "a " + t
	}
}

// lastWord returns the last space-separated word of s.
func lastWord(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return s
	}
	return fields[len(fields)-1]
}

// plainNumber renders a bound such as 0.000000 or 9007199254740991.000000
// without trailing zeros.
func plainNumber(s string) string {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return s
	}
	return strconv.FormatFloat(f, 'f', -1, 64)
}
