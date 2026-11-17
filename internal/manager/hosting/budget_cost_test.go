package hosting

import "testing"

func TestReservationCostBoundsApprovedOutputAtFrontierPrices(t *testing.T) {
	s := &Service{
		Config: Config{InputPrice: 1_000_000, OutputPrice: 5_000_000, MaxOutputTokens: 8192},
	}
	policy := &ModelPolicy{InputPrice: 8_000_000, OutputPrice: 40_000_000, MaxOutputTokens: 2048}
	for _, scenario := range []struct {
		name     string
		rawBytes int64
		want     int64
	}{
		{name: "codex_initial", rawBytes: 39705, want: 782736},
		{name: "claude_initial", rawBytes: 46597, want: 893008},
		{name: "claude_expanded_tools", rawBytes: 74540, want: 1340096},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			bound := scenario.rawBytes*2 + 8192 + policy.MaxOutputTokens
			if got := s.reservationCost(bound, policy); got != scenario.want {
				t.Fatalf("frontier reservation cost = %d, want %d", got, scenario.want)
			}
		})
	}
}

func TestReservationCostHandlesLegacyCapsAndRateOrdering(t *testing.T) {
	for _, scenario := range []struct {
		name       string
		inputRate  int64
		outputRate int64
		maximum    int64
		tokens     int64
		want       int64
	}{
		{name: "legacy_native_bound", inputRate: 2_000_000, outputRate: 10_000_000, maximum: 2048, tokens: 103434, want: 223252},
		{name: "below_output_cap", inputRate: 8_000_000, outputRate: 40_000_000, maximum: 2048, tokens: 1000, want: 40000},
		{name: "at_output_cap", inputRate: 8_000_000, outputRate: 40_000_000, maximum: 2048, tokens: 2048, want: 81920},
		{name: "past_output_cap", inputRate: 8_000_000, outputRate: 40_000_000, maximum: 2048, tokens: 2049, want: 81928},
		{name: "input_more_expensive", inputRate: 40_000_000, outputRate: 8_000_000, maximum: 2048, tokens: 10000, want: 400000},
		{name: "zero_input", inputRate: 0, outputRate: 40_000_000, maximum: 2048, tokens: 10000, want: 81920},
		{name: "zero_output", inputRate: 8_000_000, outputRate: 0, maximum: 2048, tokens: 10000, want: 80000},
		{name: "zero_prices", inputRate: 0, outputRate: 0, maximum: 2048, tokens: 10000, want: 0},
		{name: "no_tokens", inputRate: 8_000_000, outputRate: 40_000_000, maximum: 2048, tokens: 0, want: 0},
		{name: "fractional_microusd_rounds_both_bounds", inputRate: 1, outputRate: 2, maximum: 1, tokens: 2, want: 2},
		{name: "fractional_output_allocation_needs_rounding_margin", inputRate: 1, outputRate: 2, maximum: 32, tokens: 2, want: 2},
		{name: "fractional_input_allocation_needs_rounding_margin", inputRate: 2, outputRate: 1, maximum: 32, tokens: 2, want: 2},
		{name: "equal_fractional_rates_need_rounding_margin", inputRate: 1, outputRate: 1, maximum: 32, tokens: 2, want: 2},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s := &Service{Config: Config{
				InputPrice: scenario.inputRate, OutputPrice: scenario.outputRate,
				MaxOutputTokens: scenario.maximum,
			}}
			if got := s.reservationCost(scenario.tokens, nil); got != scenario.want {
				t.Fatalf("legacy reservation cost = %d, want %d", got, scenario.want)
			}
		})
	}
}

func TestReservationCostCoversEverySplitWithinApprovedOutputCap(t *testing.T) {
	for _, rates := range [][2]int64{
		{8_000_000, 40_000_000}, {40_000_000, 8_000_000}, {1, 2}, {2, 1}, {1, 1},
		{500001, 500003}, {999999, 1999999}, {1000001, 999999}, {0, 2}, {2, 0}, {0, 0},
	} {
		for _, maximum := range []int64{1, 2, 32, 64} {
			s := &Service{
				Config: Config{
					InputPrice:      rates[0],
					OutputPrice:     rates[1],
					MaxOutputTokens: maximum,
				},
			}
			for total := int64(0); total <= 64; total++ {
				reserved := s.reservationCost(total, nil)
				for output := int64(0); output <= min(total, maximum); output++ {
					actual := price(total-output, rates[0]) + price(output, rates[1])
					if actual > reserved {
						t.Fatalf(
							"reservation %d below valid split cost %d: rates=%v cap=%d total=%d output=%d",
							reserved,
							actual,
							rates,
							maximum,
							total,
							output,
						)
					}
				}
			}
		}
	}
}
