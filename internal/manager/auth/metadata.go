package auth

import "strings"

type RequestMetadata struct {
	method  string
	path    string
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

// WithTarget uses the transport request, never caller-supplied headers.
func (m RequestMetadata) WithTarget(method, path string) RequestMetadata {
	m.method, m.path = method, path
	return m
}
