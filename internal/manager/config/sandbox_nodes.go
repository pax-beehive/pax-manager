package config

import (
	"errors"
	"net/url"
	"regexp"
	"strings"
)

var sandboxHostID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

func (c Config) validateSandboxNodes() error {
	key, host := c.HostingSandboxHostKey, c.HostingSandboxHostID
	if key == "" && host == "" && c.HostingSandboxCloudURL == "" {
		return nil
	}
	if len(key) < 32 || len(key) > 4096 || strings.ContainsFunc(key, func(r rune) bool {
		return r < 33 || r > 126
	}) || key == c.HostingHostKey || key == c.HostingGatewayKey || key == c.HostingTokenSecret {
		return errors.New("HOSTING_SANDBOX_HOST_KEY must be a distinct sandbox worker credential")
	}
	if !sandboxHostID.MatchString(host) {
		return errors.New("HOSTING_SANDBOX_HOST_ID must identify the sandbox worker")
	}
	if !validSandboxNodeOrigin(c.HostingSandboxCloudURL) {
		return errors.New("HOSTING_SANDBOX_CLOUD_URL must be a public HTTPS origin")
	}
	return nil
}

func validSandboxNodeOrigin(raw string) bool {
	endpoint, err := url.Parse(raw)
	return err == nil && endpoint.Scheme == "https" && endpoint.Hostname() != "" &&
		endpoint.User == nil && endpoint.RawQuery == "" && !endpoint.ForceQuery &&
		endpoint.Fragment == "" && (endpoint.Port() == "" || endpoint.Port() == "443") &&
		(endpoint.Path == "" || endpoint.Path == "/")
}
