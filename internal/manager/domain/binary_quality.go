package domain

import "sort"

// Legacy combined tags are interpreted conservatively: disabled wins over stable.
func BinaryQuality(tags []string) string {
	state := "testing"
	for _, tag := range tags {
		if tag == "disabled" {
			return "disabled"
		}
		if tag == "stable" {
			state = "stable"
		}
	}
	return state
}

func BinaryQualityTags(tags []string) []string {
	result := []string{BinaryQuality(tags)}
	for _, tag := range tags {
		if tag != "testing" && tag != "stable" && tag != "disabled" {
			result = append(result, tag)
		}
	}
	sort.Strings(result)
	return result
}
