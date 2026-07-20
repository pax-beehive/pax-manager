package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const postgresReplacementCharacter = "\uFFFD"

type postgresJSONSanitization struct {
	NULReplacements int
}

func postgresSafeText(value string) (string, int) {
	nulReplacements := strings.Count(value, "\x00")
	value = strings.ToValidUTF8(value, postgresReplacementCharacter)
	if nulReplacements == 0 {
		return value, 0
	}
	return strings.ReplaceAll(value, "\x00", postgresReplacementCharacter), nulReplacements
}

func postgresSafeJSON(
	raw json.RawMessage,
) (json.RawMessage, postgresJSONSanitization, error) {
	if len(raw) == 0 {
		return nil, postgresJSONSanitization{}, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, postgresJSONSanitization{}, fmt.Errorf("decode JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, postgresJSONSanitization{}, fmt.Errorf("decode JSON: %w", err)
	}

	normalized, nulReplacements, err := postgresSafeJSONValue(value)
	if err != nil {
		return nil, postgresJSONSanitization{}, err
	}
	if nulReplacements == 0 && utf8.Valid(raw) && !containsJSONSurrogateEscape(raw) {
		return append(json.RawMessage(nil), raw...), postgresJSONSanitization{}, nil
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return nil, postgresJSONSanitization{}, fmt.Errorf("encode JSON: %w", err)
	}
	return data, postgresJSONSanitization{NULReplacements: nulReplacements}, nil
}

func containsJSONSurrogateEscape(raw []byte) bool {
	for i := 0; i+5 < len(raw); i++ {
		if raw[i] != '\\' || raw[i+1] != 'u' || (raw[i+2] != 'd' && raw[i+2] != 'D') {
			continue
		}
		if (raw[i+3] >= '8' && raw[i+3] <= '9') ||
			(raw[i+3] >= 'a' && raw[i+3] <= 'f') ||
			(raw[i+3] >= 'A' && raw[i+3] <= 'F') {
			return true
		}
	}
	return false
}

func postgresSafeJSONValue(value any) (any, int, error) {
	switch typed := value.(type) {
	case string:
		normalized, replacements := postgresSafeText(typed)
		return normalized, replacements, nil
	case []any:
		normalized := make([]any, len(typed))
		total := 0
		for i, item := range typed {
			clean, replacements, err := postgresSafeJSONValue(item)
			if err != nil {
				return nil, 0, err
			}
			normalized[i] = clean
			total += replacements
		}
		return normalized, total, nil
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		total := 0
		for key, item := range typed {
			cleanKey, keyReplacements := postgresSafeText(key)
			if _, exists := normalized[cleanKey]; exists {
				return nil, 0, fmt.Errorf(
					"sanitize JSON object key %q: normalized key collision",
					key,
				)
			}
			clean, replacements, err := postgresSafeJSONValue(item)
			if err != nil {
				return nil, 0, err
			}
			normalized[cleanKey] = clean
			total += keyReplacements + replacements
		}
		return normalized, total, nil
	default:
		return value, 0, nil
	}
}
