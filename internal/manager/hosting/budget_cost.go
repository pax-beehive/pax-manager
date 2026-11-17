package hosting

// reservationCost prices a total-token upper bound using the approved output
// cap. When output is more expensive, only that capped portion can use its rate.
// When input is more expensive, pricing the whole bound as input is conservative.
func (s *Service) reservationCost(tokens int64, policy *ModelPolicy) int64 {
	inputRate, outputRate, maximum := s.Config.InputPrice, s.Config.OutputPrice, s.Config.MaxOutputTokens
	if policy != nil {
		inputRate, outputRate, maximum = policy.InputPrice, policy.OutputPrice, policy.MaxOutputTokens
	}
	input, output := tokens, int64(0)
	if outputRate >= inputRate {
		output = min(tokens, maximum)
		input = tokens - output
	}
	cost := price(input, inputRate) + price(output, outputRate)
	// A different valid split can round both fractional components up, even
	// when this worst-case allocation rounds only once.
	if tokens > 1 && maximum > 0 && inputRate%1_000_000 != 0 && outputRate%1_000_000 != 0 &&
		cost == price(1, input*inputRate+output*outputRate) {
		cost++
	}
	return cost
}
