package modelcatalog

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const maxModels = 2000

var ErrUnavailable = errors.New("model_catalog_unavailable")
var modelID = regexp.MustCompile(`^[A-Za-z0-9~][A-Za-z0-9._:/@+~=-]*$`)

type Model struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	ContextLength         int64  `json:"context_length"`
	MaxOutputTokens       int64  `json:"max_output_tokens"`
	InputPrice            int64  `json:"input_microusd_per_million"`
	OutputPrice           int64  `json:"output_microusd_per_million"`
	PricingIsConservative bool   `json:"pricing_is_conservative,omitempty"`
}

func Normalize(raw []byte) ([]Model, error) {
	if len(raw) > maxCatalogBytes || !utf8.Valid(raw) {
		return nil, ErrUnavailable
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var envelope map[string]any
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF {
		return nil, ErrUnavailable
	}
	entries, valid := envelope["data"].([]any)
	if !valid || len(entries) > maxModels || !completeCatalog(envelope["links"]) {
		return nil, ErrUnavailable
	}
	models := map[string]Model{}
	duplicates := map[string]bool{}
	for _, entry := range entries {
		if model, ok := normalizeModel(entry); ok {
			if _, exists := models[model.ID]; exists {
				duplicates[model.ID] = true
			}
			models[model.ID] = model
		}
	}
	result := make([]Model, 0, len(models))
	for id, model := range models {
		if !duplicates[id] {
			result = append(result, model)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func completeCatalog(value any) bool {
	if value == nil {
		return true
	}
	links, valid := value.(map[string]any)
	return valid && links["next"] == nil
}

func normalizeModel(value any) (Model, bool) {
	entry, valid := value.(map[string]any)
	if !valid {
		return Model{}, false
	}
	id, idOK := entry["id"].(string)
	name, nameOK := entry["name"].(string)
	if !idOK || !validModelID(id) || !nameOK || !validModelName(name) ||
		!textModel(entry["architecture"]) {
		return Model{}, false
	}
	prices, pricesOK := normalizePrices(entry["pricing"])
	context, output, limitsOK := modelLimits(entry)
	if !pricesOK || !limitsOK {
		return Model{}, false
	}
	return Model{
		ID: id, Name: trimModelName(name),
		ContextLength: context, MaxOutputTokens: output,
		InputPrice: prices.input, OutputPrice: prices.output,
		PricingIsConservative: prices.conservative,
	}, true
}

func validModelID(id string) bool {
	if len(id) > 245 || !modelID.MatchString(id) || strings.Contains(id, "://") {
		return false
	}
	for _, segment := range strings.Split(id, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func validModelName(name string) bool {
	length := 0
	for _, r := range name {
		if r < 32 || r == 127 {
			return false
		}
		length += utf16.RuneLen(r)
	}
	return length <= 256 && trimModelName(name) != ""
}

func trimModelName(name string) string {
	return strings.TrimFunc(name, func(r rune) bool { return unicode.IsSpace(r) || r == '\uFEFF' })
}

func textModel(value any) bool {
	architecture, valid := value.(map[string]any)
	if !valid {
		return false
	}
	inputs, inputOK := architecture["input_modalities"].([]any)
	outputs, outputOK := architecture["output_modalities"].([]any)
	if !inputOK || !outputOK || len(outputs) != 1 || outputs[0] != "text" {
		return false
	}
	for _, input := range inputs {
		if input == "text" {
			return true
		}
	}
	return false
}

func boundedInteger(value any) (int64, bool) {
	number, valid := value.(json.Number)
	if !valid {
		return 0, false
	}
	parsed, err := number.Float64()
	return int64(
			parsed,
		), err == nil && parsed > 0 && parsed <= 1_000_000_000 &&
			math.Trunc(parsed) == parsed
}

func modelLimits(entry map[string]any) (int64, int64, bool) {
	top := map[string]any{}
	if value := entry["top_provider"]; value != nil {
		var valid bool
		top, valid = value.(map[string]any)
		if !valid {
			return 0, 0, false
		}
	}
	context := int64(0)
	for _, value := range []any{entry["context_length"], top["context_length"]} {
		if value == nil {
			continue
		}
		limit, valid := boundedInteger(value)
		if !valid {
			return 0, 0, false
		}
		if context == 0 || limit < context {
			context = limit
		}
	}
	output := context
	if output == 0 {
		output = 4096
	}
	if value := top["max_completion_tokens"]; value != nil {
		var valid bool
		output, valid = boundedInteger(value)
		if !valid {
			return 0, 0, false
		}
	}
	output = min(8192, output)
	if context > 0 {
		output = min(output, context)
	}
	return context, output, true
}
