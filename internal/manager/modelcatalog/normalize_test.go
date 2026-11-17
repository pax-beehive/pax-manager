package modelcatalog

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func catalogFixture(changes map[string]any) map[string]any {
	model := map[string]any{
		"id": "vendor/model", "name": "\u6a21\u578b \u2014 Model",
		"architecture": map[string]any{
			"input_modalities": []string{"text", "image"}, "output_modalities": []string{"text"},
		},
		"context_length": 128000,
		"top_provider":   map[string]any{"context_length": 64000, "max_completion_tokens": 16000},
		"pricing":        map[string]any{"prompt": "0.0000001234567", "completion": "0.0000025"},
	}
	for key, value := range changes {
		model[key] = value
	}
	return model
}

func normalizeFixture(t *testing.T, entries ...any) []Model {
	t.Helper()
	if entries == nil {
		entries = []any{}
	}
	return normalizeDocument(t, map[string]any{"data": entries})
}

func normalizeDocument(t *testing.T, document any) []Model {
	t.Helper()
	raw, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	models, err := Normalize(raw)
	if err != nil {
		t.Fatalf("valid fixture rejected: %v", err)
	}
	return models
}

func TestTokenPriceExactConservativeRounding(t *testing.T) {
	cases := []struct {
		value string
		want  int64
	}{
		{"0", 0}, {"0.000001", 1000000}, {"1.234567e-7", 123457},
		{"0.0000000000010000000000001", 2}, {"1e-1000", 1},
		{"1", 1000000000000}, {"0e1000", 0}, {"1E-12", 1},
		{"0.000000000000000001", 1}, {"0.000000999999999999999999", 1000000},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			got, ok := TokenPrice(tc.value)
			if !ok || got != tc.want {
				t.Fatalf("TokenPrice(%q) = %d, %v; want %d, true", tc.value, got, ok, tc.want)
			}
		})
	}
}

func TestTokenPriceRejectsUnknownNegativeAndUnboundedValues(t *testing.T) {
	values := []any{
		"-1", "-0", "+1", "1.000000000000000001", "NaN", "Infinity", "", " 0", "0 ",
		"0x10", "1e999999", "0e1001", "1e-1001", "01", strings.Repeat("0", 97),
		0, float64(0), nil, true, []string{"0"}, map[string]any{"price": "0"},
	}
	for index, value := range values {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			if got, ok := TokenPrice(value); ok {
				t.Fatalf("invalid price accepted: %#v -> %d", value, got)
			}
		})
	}
}

func TestNormalizeReturnsOnlyPublicFieldsWithUnicodeNames(t *testing.T) {
	models := normalizeDocument(t, map[string]any{
		"data": []any{catalogFixture(map[string]any{
			"description": "untrusted HTML", "links": map[string]any{"details": "https://never-follow.example"},
		})},
		"links": map[string]any{"next": nil},
	})
	want := []Model{{ID: "vendor/model", Name: "\u6a21\u578b \u2014 Model", ContextLength: 64000,
		MaxOutputTokens: 8192, InputPrice: 123457, OutputPrice: 2500000}}
	if !reflect.DeepEqual(models, want) {
		t.Fatalf("normalized model differs: got %+v; want %+v", models, want)
	}
	raw, err := json.Marshal(models)
	if err != nil {
		t.Fatal(err)
	}
	var fields []map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields[0]) != 6 || strings.Contains(string(raw), "never-follow") ||
		strings.Contains(string(raw), "untrusted") {
		t.Fatal("upstream fields escaped public normalization")
	}
}

func TestNormalizeUsesProviderContextAndSafeOutputFallbacks(t *testing.T) {
	cases := []struct {
		name    string
		changes map[string]any
		context int64
		output  int64
	}{
		{
			"context-smaller-than-output",
			map[string]any{
				"context_length": 1000,
				"top_provider":   map[string]any{"max_completion_tokens": 20000},
			},
			1000,
			1000,
		},
		{
			"provider-context-smaller",
			map[string]any{
				"top_provider": map[string]any{
					"context_length":        512,
					"max_completion_tokens": 2048,
				},
			},
			512,
			512,
		},
		{
			"provider-output-smaller",
			map[string]any{"top_provider": map[string]any{"max_completion_tokens": 123}},
			128000,
			123,
		},
		{"unknown-context", map[string]any{"context_length": nil, "top_provider": nil}, 0, 4096},
		{
			"null-output",
			map[string]any{
				"context_length": 1024,
				"top_provider":   map[string]any{"max_completion_tokens": nil},
			},
			1024,
			1024,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			models := normalizeFixture(t, catalogFixture(tc.changes))
			if len(models) != 1 || models[0].ContextLength != tc.context ||
				models[0].MaxOutputTokens != tc.output {
				t.Fatalf("unsafe context/output normalization: %+v", models)
			}
		})
	}
}

func TestNormalizeConservativeConditionalCacheAndReasoningRates(t *testing.T) {
	models := normalizeFixture(t, catalogFixture(map[string]any{"pricing": map[string]any{
		"prompt": "0.000001", "completion": "0.000002", "input_cache_write": "0.00000125", "request": "0",
		"overrides": []any{
			map[string]any{
				"min_prompt_tokens": 200000,
				"prompt":            "0.000003",
				"completion":        "0.000004",
			},
			map[string]any{
				"utc_start":          30,
				"utc_end":            1630,
				"utc_days":           []string{"monday"},
				"prompt":             "0.000002",
				"internal_reasoning": "0.000005",
			},
		},
	}}))
	if len(models) != 1 || models[0].InputPrice != 3000000 || models[0].OutputPrice != 5000000 ||
		!models[0].PricingIsConservative {
		t.Fatalf("conditional rates were underestimated: %+v", models)
	}
	models = normalizeFixture(t, catalogFixture(map[string]any{"pricing": map[string]any{
		"prompt": "0.000001", "completion": "0.000002", "input_cache_write_1h": "0.000003",
	}}))
	if len(models) != 1 || models[0].InputPrice != 3000000 || !models[0].PricingIsConservative {
		t.Fatalf("cache-write token rate was underestimated: %+v", models)
	}
	models = normalizeFixture(t, catalogFixture(map[string]any{"pricing": map[string]any{
		"prompt": "0.000001", "completion": "0.000002", "input_cache_read": "0.0000001",
		"overrides": []any{map[string]any{"min_prompt_tokens": 1000, "prompt": "0.0000005"}},
	}}))
	if len(models) != 1 || models[0].InputPrice != 1000000 || models[0].PricingIsConservative {
		t.Fatalf("base rate was lowered or incorrectly flagged: %+v", models)
	}
}

func TestNormalizeExcludesUnknownOrUnaccountablePricing(t *testing.T) {
	cases := []map[string]any{
		{"prompt": "-1", "completion": "0"},
		{"prompt": "0", "completion": "unknown"},
		{"prompt": "0", "completion": "0", "request": "0.00000000000001"},
		{"prompt": "0", "completion": "0", "future_fee": "0.1"},
		{"prompt": "0", "completion": "0", "image": "-1"},
		{"prompt": "0", "completion": "0", "overrides": []any{map[string]any{"request": "0.01"}}},
		{
			"prompt":     "0",
			"completion": "0",
			"overrides":  []any{map[string]any{"new_charge": "0.01"}},
		},
		{"prompt": "0", "completion": "0", "overrides": []any{map[string]any{"prompt": "-1"}}},
		{"prompt": "0", "completion": "0", "overrides": "unknown"},
		{"prompt": "0", "completion": "0", "overrides": []any{nil}},
		{"prompt": "0"},
	}
	for index, pricing := range cases {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			if models := normalizeFixture(t, catalogFixture(map[string]any{"pricing": pricing})); len(
				models,
			) != 0 {
				t.Fatalf("unaccountable pricing was assumed free: %+v", models)
			}
		})
	}
}

func TestNormalizeExcludesUnsupportedModalitiesIdentifiersAndLimits(t *testing.T) {
	changes := []map[string]any{
		{
			"architecture": map[string]any{
				"input_modalities":  []string{"image"},
				"output_modalities": []string{"text"},
			},
		},
		{
			"architecture": map[string]any{
				"input_modalities":  []string{"text"},
				"output_modalities": []string{"text", "image"},
			},
		},
		{
			"architecture": map[string]any{
				"input_modalities":  []string{"text"},
				"output_modalities": []string{},
			},
		},
		{"id": "bad id"}, {"id": strings.Repeat("x", 246)}, {"id": "vendor//model"},
		{"id": "vendor/../model"}, {"id": "vendor/./model"}, {"id": "vendor/%2e%2e/model"},
		{
			"id": "vendor/model?secret=bad",
		}, {"id": "vendor\\model"}, {"id": "https://bad.example/model"},
		{"name": "bad\x00name"}, {"name": "bad\nname"}, {"name": strings.Repeat("x", 257)},
		{"name": ""}, {"name": "   "}, {"name": " \uFEFF "},
		{"context_length": 1.5}, {"context_length": -1}, {"context_length": 1000000001},
		{"top_provider": map[string]any{"max_completion_tokens": -1}}, {"top_provider": false},
	}
	for index, change := range changes {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			if models := normalizeFixture(t, catalogFixture(change)); len(models) != 0 {
				t.Fatalf("unsupported model was accepted: %+v", models)
			}
		})
	}
	models := normalizeFixture(
		t,
		catalogFixture(map[string]any{"id": "v/" + strings.Repeat("x", 243)}),
	)
	if len(models) != 1 {
		t.Fatal("245-character importable OpenRouter model ID was rejected")
	}
}

func TestNormalizeRejectsDuplicateIDsInsteadOfChoosingACheaperAlias(t *testing.T) {
	models := normalizeFixture(t,
		catalogFixture(nil),
		catalogFixture(map[string]any{"pricing": map[string]any{"prompt": "0", "completion": "0"}}),
		catalogFixture(map[string]any{"id": "vendor/unique"}),
	)
	if len(models) != 1 || models[0].ID != "vendor/unique" {
		t.Fatalf("ambiguous duplicate model survived: %+v", models)
	}
}

func TestNormalizeRejectsMalformedPartialAndOversizedCatalogs(t *testing.T) {
	documents := []any{
		nil, map[string]any{}, map[string]any{"data": map[string]any{}},
		map[string]any{
			"data":  []any{catalogFixture(nil)},
			"links": map[string]any{"next": "https://never-follow.example/models"},
		},
		map[string]any{"data": []any{catalogFixture(nil)}, "links": "unknown"},
		map[string]any{"data": make([]any, 2001)},
	}
	for index, document := range documents {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Normalize(raw); err == nil {
				t.Fatal("malformed, paginated or oversized catalog was accepted")
			}
		})
	}
	for _, raw := range []string{"{", `{"data":[]} {}`, `{"data":[],"links":{"next":false}}`} {
		if _, err := Normalize([]byte(raw)); err == nil {
			t.Fatalf("invalid JSON/catalog accepted: %s", raw)
		}
	}
}

func TestNormalizeAcceptsBoundedCatalogAndSortsDeterministically(t *testing.T) {
	entries := make([]any, 2000)
	for index := range entries {
		entries[index] = catalogFixture(
			map[string]any{"id": fmt.Sprintf("vendor/model-%04d", 1999-index)},
		)
	}
	models := normalizeFixture(t, entries...)
	if len(models) != 2000 || models[0].ID != "vendor/model-0000" ||
		models[1999].ID != "vendor/model-1999" {
		t.Fatal("bounded catalog was truncated or sorted nondeterministically")
	}
	if models := normalizeFixture(t); len(models) != 0 {
		t.Fatal("empty catalog invented models")
	}
}
