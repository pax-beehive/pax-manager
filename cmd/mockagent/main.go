// mock-agent registers with pax-manager, polls the mailbox API, and reports
// completed results. It is a local stand-in for paxd when Hermes is not needed.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"
)

type registerResponse struct {
	AgentID string `json:"agentId"`
	APIKey  string `json:"apiKey"`
}

type mailboxPull struct {
	Messages  []mailboxMessage `json:"messages"`
	MaxOffset int64            `json:"max_offset"`
	HasMore   bool             `json:"has_more"`
}

type mailboxMessage struct {
	ID          int64           `json:"id"`
	MessageID   string          `json:"messageId"`
	SessionID   string          `json:"sessionId"`
	Message     string          `json:"message"`
	MessageType string          `json:"messageType"`
	Payload     json.RawMessage `json:"payload"`
}

func main() {
	baseURL := envDefault("MANAGER_URL", "http://localhost:9879")
	userEmail := envDefault("MOCK_USER_EMAIL", "local@example.local")
	client := &http.Client{Timeout: 10 * time.Second}

	hostname, _ := os.Hostname()
	token := createRegistrationToken(client, baseURL, userEmail)
	registered := register(client, baseURL, hostname, token)
	log.Printf("registered mock agent %s", registered.AgentID)

	done := make(chan os.Signal, 1)
	signal.Notify(done, os.Interrupt)

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	var offset int64
	for {
		select {
		case <-done:
			log.Printf("shutting down")
			return
		case <-ticker.C:
			postStatus(client, baseURL, registered)
			pull := getMailbox(client, baseURL, registered, offset)
			for _, msg := range pull.Messages {
				log.Printf("received %s message %s for session %s", msg.MessageType, msg.MessageID, msg.SessionID)
				result := fmt.Sprintf("mock agent processed: %s", msg.Message)
				postResult(client, baseURL, registered, msg.MessageID, result)
			}
			if pull.MaxOffset > offset {
				offset = pull.MaxOffset
				postOffset(client, baseURL, registered, offset)
			}
		}
	}
}

func createRegistrationToken(client *http.Client, baseURL, userEmail string) string {
	var out CreateRegistrationTokenResponse
	doJSON(client, http.MethodPost, baseURL+"/api/user/agent-registration-tokens", "", map[string]any{}, &out, userEmail)
	return out.Token
}

type CreateRegistrationTokenResponse struct {
	Token string `json:"token"`
}

func register(client *http.Client, baseURL, hostname, registrationToken string) registerResponse {
	body := map[string]any{
		"name":      "mock-agent-" + hostname,
		"hostname":  hostname,
		"agentType": "hermes",
		"os":        "unknown",
	}
	var out registerResponse
	doJSONWithHeaders(client, http.MethodPost, baseURL+"/api/agent/register", "", body, &out, map[string]string{
		"X-Registration-Token": registrationToken,
	})
	return out
}

func postStatus(client *http.Client, baseURL string, registered registerResponse) {
	body := map[string]any{
		"agent_id": registered.AgentID,
		"sessions": []map[string]any{
			{
				"sessionId":    "mock-session",
				"agentType":    "hermes",
				"name":         "mock session",
				"status":       "idle",
				"messageCount": 0,
				"tokenUsage":   0,
			},
		},
	}
	doJSON(client, http.MethodPost, baseURL+"/api/agent/status", registered.APIKey, body, nil, "")
}

func getMailbox(client *http.Client, baseURL string, registered registerResponse, offset int64) mailboxPull {
	var out mailboxPull
	url := fmt.Sprintf("%s/api/agent/mailbox?offset=%d&limit=10", baseURL, offset)
	doJSON(client, http.MethodGet, url, registered.APIKey, nil, &out, "")
	return out
}

func postResult(client *http.Client, baseURL string, registered registerResponse, messageID, result string) {
	body := map[string]any{"status": "completed", "result": result}
	url := fmt.Sprintf("%s/api/agent/messages/%s/result", baseURL, messageID)
	doJSON(client, http.MethodPost, url, registered.APIKey, body, nil, "")
}

func postOffset(client *http.Client, baseURL string, registered registerResponse, offset int64) {
	body := map[string]any{"offset": offset}
	doJSON(client, http.MethodPost, baseURL+"/api/agent/messages/offset", registered.APIKey, body, nil, "")
}

func doJSON(client *http.Client, method, url, apiKey string, body any, out any, userEmail string) {
	headers := map[string]string{}
	if userEmail != "" {
		headers["Cf-Access-Authenticated-User-Email"] = userEmail
	}
	doJSONWithHeaders(client, method, url, apiKey, body, out, headers)
}

func doJSONWithHeaders(client *http.Client, method, url, apiKey string, body any, out any, headers map[string]string) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			log.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		log.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Fatalf("%s %s: HTTP %d: %s", method, url, resp.StatusCode, string(raw))
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			log.Fatalf("decode: %v: %s", err, string(raw))
		}
	}
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
