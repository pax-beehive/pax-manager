package auth

import "strings"

type RequestMetadata struct {
	headers map[string]string
}

func NewRequestMetadata(headers map[string]string) RequestMetadata {
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[strings.ToLower(k)] = strings.TrimSpace(v)
	}
	return RequestMetadata{headers: out}
}

func (m RequestMetadata) Header(name string) string {
	if m.headers == nil {
		return ""
	}
	return m.headers[strings.ToLower(name)]
}
