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
	if key == "" && host == "" && c.HostingSandboxCloudURL == "" && c.HostingSandboxOwners == "" {
		return nil
	}
	if len(key) < 32 || len(key) > 4096 || strings.ContainsFunc(key, func(r rune) bool {
		return r < 33 || r > 126
	}) || key == c.HostingHostKey || key == c.HostingGatewayKey || key == c.HostingTokenSecret {
		return errors.New("HOSTING_SANDBOX_HOST_KEY must be a distinct sandbox worker credential")
	}
	if !sandboxHostID.MatchString(host) || len(c.SandboxOwners()) == 0 {
		return errors.New("sandbox Nodes require a host ID and at least one allowed user ID")
	}
	endpoint, err := url.Parse(c.HostingSandboxCloudURL)
	if err != nil || endpoint.Scheme != "https" || endpoint.Hostname() == "" ||
		endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery ||
		endpoint.Fragment != "" || (endpoint.Port() != "" && endpoint.Port() != "443") ||
		(endpoint.Path != "" && endpoint.Path != "/") {
		return errors.New("HOSTING_SANDBOX_CLOUD_URL must be a public HTTPS origin")
	}
	return nil
}

func (c Config) SandboxOwners() []string {
	owners := []string{}
	for _, entry := range strings.Split(c.HostingSandboxOwners, ",") {
		if owner := strings.TrimSpace(entry); owner != "" {
			owners = append(owners, owner)
		}
	}
	return owners
}
