package modelcatalog

import (
	"math/big"
	"regexp"
	"strconv"
)

var decimalPrice = regexp.MustCompile(`^(0|[1-9][0-9]*)(?:\.([0-9]+))?(?:[eE]([+-]?[0-9]+))?$`)

// TokenPrice rounds decimal USD/token upward to microusd per million tokens.
func TokenPrice(value any) (int64, bool) {
	raw, ok := value.(string)
	if !ok || len(raw) > 96 {
		return 0, false
	}
	parts := decimalPrice.FindStringSubmatch(raw)
	if parts == nil {
		return 0, false
	}
	exponent := 0
	if parts[3] != "" {
		var err error
		exponent, err = strconv.Atoi(parts[3])
		if err != nil || exponent < -1000 || exponent > 1000 {
			return 0, false
		}
	}
	digits, _ := new(big.Int).SetString(parts[1]+parts[2], 10)
	scale := 12 + exponent - len(parts[2])
	power := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(abs(scale))), nil)
	if scale >= 0 {
		digits.Mul(digits, power)
	} else {
		remainder := new(big.Int)
		digits.QuoRem(digits, power, remainder)
		if remainder.Sign() > 0 {
			digits.Add(digits, big.NewInt(1))
		}
	}
	if !digits.IsInt64() || digits.Int64() > 1_000_000_000_000 {
		return 0, false
	}
	return digits.Int64(), true
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

type modelPrices struct {
	input, output int64
	conservative  bool
}

func normalizePrices(value any) (modelPrices, bool) {
	base, ok := value.(map[string]any)
	if !ok {
		return modelPrices{}, false
	}
	prompt, promptOK := TokenPrice(base["prompt"])
	completion, completionOK := TokenPrice(base["completion"])
	if !promptOK || !completionOK {
		return modelPrices{}, false
	}
	variants := []any{}
	if raw := base["overrides"]; raw != nil {
		var valid bool
		variants, valid = raw.([]any)
		if !valid || len(variants) > 100 {
			return modelPrices{}, false
		}
	}
	result := modelPrices{input: prompt, output: completion}
	for index, raw := range append([]any{base}, variants...) {
		rates, valid := raw.(map[string]any)
		if !valid || !accumulatePrices(rates, index > 0, &result) {
			return modelPrices{}, false
		}
	}
	result.conservative = result.input > prompt || result.output > completion
	return result, true
}

func accumulatePrices(rates map[string]any, conditional bool, result *modelPrices) bool {
	for key, raw := range rates {
		if !conditional && key == "overrides" {
			continue
		}
		if conditional && knownCondition(key) {
			continue
		}
		if !knownPrice(key) {
			return false
		}
		amount, valid := TokenPrice(raw)
		if !valid || (key == "request" && amount > 0) {
			return false
		}
		switch key {
		case "prompt", "input_cache_read", "input_cache_write", "input_cache_write_1h":
			result.input = max(result.input, amount)
		case "completion", "internal_reasoning":
			result.output = max(result.output, amount)
		}
	}
	return true
}

func knownPrice(key string) bool {
	switch key {
	case "prompt", "completion", "input_cache_read", "input_cache_write", "input_cache_write_1h",
		"internal_reasoning", "request", "image", "image_output", "audio", "audio_output",
		"input_audio_cache", "web_search":
		return true
	default:
		return false
	}
}

func knownCondition(key string) bool {
	switch key {
	case "min_prompt_tokens", "utc_start", "utc_end", "utc_days":
		return true
	default:
		return false
	}
}
