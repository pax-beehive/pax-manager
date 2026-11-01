package domain

import (
	"sort"
	"strings"
)

const MaxSearchTerms = 16

// SearchTerms defines the shared literal keyword contract for index and source reads.
func SearchTerms(query string) []string {
	var terms []string
	seen := make(map[string]bool)
	for _, field := range strings.Fields(query) {
		term := strings.ToLower(field)
		if !seen[term] {
			seen[term] = true
			terms = append(terms, term)
		}
	}
	sort.Strings(terms)
	return terms
}
