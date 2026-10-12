package paxd

import (
	"bytes"
	_ "embed"
	"fmt"
	"net"
	"net/url"
	"strings"
	"text/template"
)

// SetupConfig is deployment-owned. Never infer these origins from a request Host.
type SetupConfig struct {
	DownloadURL string
	MachineURL  string
}

type SetupInfo struct {
	ScriptURL  string `json:"script_url"`
	MachineURL string `json:"machine_url"`
}

func (c SetupConfig) Validate() error {
	for name, value := range map[string]string{"download": c.DownloadURL, "machine": c.MachineURL} {
		u, err := url.Parse(value)
		if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
			(u.Path != "" && u.Path != "/") {
			return fmt.Errorf("device setup %s URL must be a public origin", name)
		}
		ip := net.ParseIP(u.Hostname())
		loopback := u.Hostname() == "localhost" || (ip != nil && ip.IsLoopback())
		if u.Scheme != "https" && (u.Scheme != "http" || !loopback) {
			return fmt.Errorf(
				"device setup %s URL requires HTTPS (HTTP loopback is allowed for tests)",
				name,
			)
		}
	}
	return nil
}

func (c SetupConfig) Info() (SetupInfo, error) {
	if err := c.Validate(); err != nil {
		return SetupInfo{}, err
	}
	return SetupInfo{
		ScriptURL: strings.TrimRight(
			c.DownloadURL,
			"/",
		) + "/api/v1/public/paxd/install.sh?setup=1",
		MachineURL: strings.TrimRight(c.MachineURL, "/"),
	}, nil
}

//go:embed setup.sh
var setupSource string

var setupTemplate = template.Must(template.New("setup").Funcs(template.FuncMap{
	"quote": func(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" },
}).Parse(setupSource))

func (c SetupConfig) Script() (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	var out bytes.Buffer
	err := setupTemplate.Execute(&out, SetupConfig{
		DownloadURL: strings.TrimRight(c.DownloadURL, "/"),
		MachineURL:  strings.TrimRight(c.MachineURL, "/"),
	})
	return out.String(), err
}
