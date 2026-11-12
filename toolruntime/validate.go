package toolruntime

import (
	"encoding/json"
	"github.com/xeipuuv/gojsonschema"
)

// ValidateInput checks untrusted arguments without resolving remote or file schemas.
func ValidateInput(schema json.RawMessage, arguments json.RawMessage) (map[string]any, error) {
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if len(arguments) > 1<<20 {
		return nil, fault("validation_error", "Tool arguments exceed the size limit.")
	}
	var args map[string]any
	if err := decodeStrict(arguments, &args); err != nil || args == nil || reservedArgument(args) {
		return nil, fault("validation_error", "Invalid tool arguments or reserved identity fields.")
	}
	var raw map[string]any
	if err := decodeStrict(schema, &raw); err != nil || raw["type"] != "object" || hasSchemaReference(raw) {
		return nil, fault("validation_error", "Tool schema is unavailable.")
	}
	loader := gojsonschema.NewSchemaLoader()
	loader.AutoDetect = false
	loader.Draft = gojsonschema.Draft7
	compiled, err := loader.Compile(gojsonschema.NewGoLoader(raw))
	if err != nil {
		return nil, fault("validation_error", "Tool schema is unavailable.")
	}
	if err := validateArguments(compiled, args); err != nil {
		return nil, err
	}
	return args, nil
}
