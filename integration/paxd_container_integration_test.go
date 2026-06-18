//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const defaultPaxdIntegrationImage = "paxd-integration:local"

type registerNodeResponse struct {
	NodeID string `json:"node_id"`
	APIKey string `json:"api_key"`
}

type createNodeAgentResponse struct {
	Agent agent `json:"agent"`
}

type nodeAgentListResponse struct {
	Agents []agent `json:"agents"`
}

func TestPaxdContainerMockHermesIntegration(t *testing.T) {
	fixture := newIntegrationFixture(t)
	var node registerNodeResponse
	var createdAgent createNodeAgentResponse
	sessionID := "sess-paxd-container"
	var userMessage mailboxMessage
	var paxdContainerName string

	t.Cleanup(func() {
		if paxdContainerName == "" {
			return
		}
		logs := dockerOutput(t, false, "logs", paxdContainerName)
		if strings.TrimSpace(logs) != "" {
			t.Logf("paxd container logs:\n%s", logs)
		}
		_ = dockerOutput(t, false, "rm", "-f", paxdContainerName)
	})

	t.Run(
		"Given pax-manager is healthy when the paxd driver starts then the API is ready",
		func(t *testing.T) {
			fixture.waitForHealth(t)
		},
	)

	t.Run(
		"Given a local user when the driver prepares node credentials then a node and cloud agent exist",
		func(t *testing.T) {
			token := postJSON[registrationTokenResponse](
				t,
				fixture,
				"/api/v1/user/self/node-registration-tokens",
				map[string]any{"expires_in_seconds": 600},
				fixture.userHeaders(),
				http.StatusOK,
			)
			if token.Token == "" {
				t.Fatalf("missing node registration token: %+v", token)
			}

			node = postJSON[registerNodeResponse](
				t,
				fixture,
				"/api/v1/node/register",
				map[string]any{
					"name":         "paxd-container-node",
					"hostname":     "paxd-container-node",
					"machine_type": "docker",
					"os":           "linux",
					"arch":         "amd64",
				},
				map[string]string{"X-Registration-Token": token.Token},
				http.StatusOK,
			)
			if node.NodeID == "" || node.APIKey == "" {
				t.Fatalf("bad node registration response: %+v", node)
			}

			createdAgent = postJSON[createNodeAgentResponse](
				t,
				fixture,
				"/api/v1/user/self/nodes/"+node.NodeID+"/agents",
				map[string]any{"name": "mock-hermes", "agent_type": "hermes"},
				fixture.userHeaders(),
				http.StatusOK,
			)
			if createdAgent.Agent.AgentID == "" {
				t.Fatalf("bad agent response: %+v", createdAgent)
			}
		},
	)

	t.Run(
		"Given a prepared node and agent when paxd starts then it reports the hosted agent online",
		func(t *testing.T) {
			image := buildPaxdIntegrationImage(t)
			paxdContainerName = startPaxdContainer(
				t,
				fixture,
				image,
				node,
				createdAgent.Agent.AgentID,
			)

			waitFor(
				t,
				60*time.Second,
				500*time.Millisecond,
				"paxd to report agent online",
				func() bool {
					agents := getJSON[nodeAgentListResponse](
						t,
						fixture,
						"/api/v1/user/self/nodes/"+node.NodeID+"/agents",
						fixture.userHeaders(),
						http.StatusOK,
					)
					for _, got := range agents.Agents {
						if got.AgentID == createdAgent.Agent.AgentID && got.Status == "online" {
							return true
						}
					}
					return false
				},
			)
		},
	)

	t.Run(
		"Given paxd and mock Hermes are running when a user sends chat then paxd completes the mailbox round trip",
		func(t *testing.T) {
			_ = postJSON[session](
				t,
				fixture,
				"/api/v1/user/self/nodes/"+node.NodeID+"/agents/"+
					createdAgent.Agent.AgentID+"/sessions",
				map[string]any{"session_id": sessionID, "name": "paxd container session"},
				fixture.userHeaders(),
				http.StatusOK,
			)

			fixture.postExpectError(
				t,
				"/api/v1/user/self/nodes/node_wrong/agents/"+
					createdAgent.Agent.AgentID+"/sessions/"+sessionID+"/messages",
				map[string]any{
					"message":      "cross node",
					"message_type": "chat",
				},
				fixture.userHeaders(),
				http.StatusNotFound,
			)

			userMessage = postJSON[mailboxMessage](
				t,
				fixture,
				"/api/v1/user/self/nodes/"+node.NodeID+"/agents/"+
					createdAgent.Agent.AgentID+"/sessions/"+sessionID+"/messages",
				map[string]any{
					"message":      "run the paxd container integration",
					"message_type": "chat",
				},
				fixture.userHeaders(),
				http.StatusOK,
			)
			if userMessage.MessageID == "" {
				t.Fatalf("bad message response: %+v", userMessage)
			}

			waitFor(
				t,
				60*time.Second,
				500*time.Millisecond,
				"paxd to complete chat message",
				func() bool {
					messages := listNodeSessionMessages(
						t,
						fixture,
						node.NodeID,
						createdAgent.Agent.AgentID,
						sessionID,
					).Messages
					original, outbound := findRoundTripMessages(messages, userMessage.MessageID)
					return original != nil &&
						original.Status == "completed" &&
						outbound != nil &&
						outbound.Status == "completed" &&
						strings.Contains(outbound.Message, "mock")
				},
			)
		},
	)
}

func buildPaxdIntegrationImage(t *testing.T) string {
	t.Helper()
	image := os.Getenv("PAXD_INTEGRATION_IMAGE")
	if image == "" {
		image = defaultPaxdIntegrationImage
	}
	if os.Getenv("PAXD_INTEGRATION_SKIP_BUILD") == "true" {
		return image
	}
	paxdDir := paxdRepoDir(t)
	runCommand(
		t,
		paxdDir,
		"docker",
		"build",
		"-f",
		"Dockerfile.integration",
		"-t",
		image,
		".",
	)
	return image
}

func startPaxdContainer(
	t *testing.T,
	fixture *integrationFixture,
	image string,
	node registerNodeResponse,
	agentID string,
) string {
	t.Helper()
	containerName := fmt.Sprintf("paxd-it-%d", time.Now().UnixNano())
	cloudURL := dockerReachableBaseURL(t, fixture.baseURL)
	args := []string{
		"run",
		"--rm",
		"-d",
		"--name",
		containerName,
		"--add-host",
		"host.docker.internal:host-gateway",
		"-e",
		"PAX_CLOUD_URL=" + cloudURL,
		"-e",
		"PAX_NODE_ID=" + node.NodeID,
		"-e",
		"PAX_NODE_API_KEY=" + node.APIKey,
		"-e",
		"PAX_AGENT_ID=" + agentID,
		"-e",
		"PAX_NODE_NAME=paxd-container-node",
		"-e",
		"PAX_MACHINE_TYPE=docker",
		"-e",
		"HERMES_API_ENDPOINT=http://127.0.0.1:8642",
		"-e",
		"PAXD_DB_PATH=/data/paxd.db",
		image,
	}
	runCommand(t, "", "docker", args...)
	return containerName
}

func listNodeSessionMessages(
	t *testing.T,
	fixture *integrationFixture,
	nodeID string,
	agentID string,
	sessionID string,
) mailboxListResponse {
	t.Helper()
	return getJSON[mailboxListResponse](
		t,
		fixture,
		"/api/v1/user/self/nodes/"+nodeID+"/agents/"+agentID+"/sessions/"+sessionID+"/messages",
		fixture.userHeaders(),
		http.StatusOK,
	)
}

func findRoundTripMessages(
	messages []mailboxMessage,
	parentMessageID string,
) (*mailboxMessage, *mailboxMessage) {
	var original *mailboxMessage
	var outbound *mailboxMessage
	for i := range messages {
		msg := &messages[i]
		switch {
		case msg.MessageID == parentMessageID:
			original = msg
		case msg.Direction == "node_to_user" && msg.ParentMessageID == parentMessageID:
			outbound = msg
		}
	}
	return original, outbound
}

func dockerReachableBaseURL(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse base URL: %v", err)
	}
	host := parsed.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		port := parsed.Port()
		parsed.Host = "host.docker.internal"
		if port != "" {
			parsed.Host += ":" + port
		}
	}
	return parsed.String()
}

func paxdRepoDir(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", "paxd"))
}

func waitFor(
	t *testing.T,
	timeout time.Duration,
	interval time.Duration,
	description string,
	check func() bool,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(interval)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func runCommand(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	output := dockerOutputInDir(t, true, dir, name, args...)
	if strings.TrimSpace(output) != "" {
		t.Logf("%s %s:\n%s", name, strings.Join(args, " "), output)
	}
}

func dockerOutput(t *testing.T, fail bool, args ...string) string {
	t.Helper()
	return dockerOutputInDir(t, fail, "", "docker", args...)
}

func dockerOutputInDir(
	t *testing.T,
	fail bool,
	dir string,
	name string,
	args ...string,
) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if fail && err != nil {
		t.Fatalf("%s %s failed: %v\n%s", name, strings.Join(args, " "), err, out.String())
	}
	return out.String()
}
