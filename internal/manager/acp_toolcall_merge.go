package manager

import (
	"bytes"
	"encoding/json"
)

// mergeACPToolCallFrames deep-merges a later ACP tool_call_update frame (patch)
// into an accumulated frame (base) so that a single history row can represent
// the full lifecycle of one toolCallId.
//
// ACP tool_call_update frames are partial patches: a later frame typically
// carries only the fields that changed (for example just "status"), so it must
// not clobber fields introduced by an earlier frame (title, kind, rawInput).
// The merge therefore uses "set-if-present" semantics identical to
// mergeRuntimeToolCall: a patch value overwrites the base only when it is
// meaningful (non-null, non-empty). Objects merge recursively; scalars and
// arrays (for example the cumulative tool output) are replaced by the latest
// meaningful value. The result is deterministic and idempotent — replaying the
// same patch never changes the accumulated frame.
func mergeACPToolCallFrames(base, patch json.RawMessage) json.RawMessage {
	if len(base) == 0 {
		return cloneRawJSON(patch)
	}
	if len(patch) == 0 {
		return cloneRawJSON(base)
	}
	baseValue, err := decodeJSONPreservingNumbers(base)
	if err != nil {
		return cloneRawJSON(patch)
	}
	patchValue, err := decodeJSONPreservingNumbers(patch)
	if err != nil {
		return cloneRawJSON(base)
	}
	merged := mergeJSONValues(baseValue, patchValue)
	out, err := json.Marshal(merged)
	if err != nil {
		return cloneRawJSON(patch)
	}
	return out
}

// mergeJSONValues recursively merges patch into base with set-if-present
// semantics. Two objects merge key-by-key; anything else is replaced by the
// patch when the patch value is meaningful, otherwise the base is kept.
func mergeJSONValues(base, patch any) any {
	patchMap, patchIsMap := patch.(map[string]any)
	baseMap, baseIsMap := base.(map[string]any)
	if patchIsMap && baseIsMap {
		out := make(map[string]any, len(baseMap)+len(patchMap))
		for key, value := range baseMap {
			out[key] = value
		}
		for key, patchValue := range patchMap {
			if !jsonValueMeaningful(patchValue) {
				continue
			}
			if baseValue, ok := out[key]; ok {
				out[key] = mergeJSONValues(baseValue, patchValue)
				continue
			}
			out[key] = patchValue
		}
		return out
	}
	if !jsonValueMeaningful(patch) {
		return base
	}
	return patch
}

// jsonValueMeaningful reports whether a decoded JSON value should overwrite an
// existing value. Empty strings, empty containers and null are treated as
// "absent" so partial patches cannot erase earlier fields; numbers (including
// zero) and booleans (including false) are always meaningful.
func jsonValueMeaningful(v any) bool {
	switch typed := v.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// decodeJSONPreservingNumbers decodes JSON while keeping numeric literals as
// json.Number so large integers survive the merge round-trip without being
// reformatted into floating-point exponents.
func decodeJSONPreservingNumbers(raw json.RawMessage) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	return value, nil
}
