// mock-agent: Minimal test agent that connects to pax-manager,
// registers, receives messages, and echoes back mock responses.
//
// Usage:
//
//	go run ./cmd/mockagent
//
// Connects to pax-manager at ws://localhost:9879/ws.
// Simulates a paxd agent without needing Hermes.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	"github.com/gorilla/websocket"
)

func main() {
	managerURL := "ws://localhost:9879/ws"
	if u := os.Getenv("MANAGER_URL"); u != "" {
		managerURL = u
	}

	log.Printf("🤖 mock-agent connecting to %s", managerURL)

	conn, _, err := websocket.DefaultDialer.Dial(managerURL, nil)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Register
	hostname, _ := os.Hostname()
	registerMsg := map[string]interface{}{
		"type":      "register",
		"name":      "mock-agent-" + hostname,
		"agentType": "hermes",
		"hostname":  hostname,
		"projects":  []map[string]string{{"id": "test", "name": "test-project", "rootPath": "/tmp"}},
	}
	if err := conn.WriteJSON(registerMsg); err != nil {
		log.Fatalf("register: %v", err)
	}

	// Wait for ack
	var ack map[string]interface{}
	if err := conn.ReadJSON(&ack); err != nil {
		log.Fatalf("read ack: %v", err)
	}
	log.Printf("registered as %v", ack["agentId"])

	// Message loop
	done := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)

	go func() {
		for {
			_, raw, err := conn.ReadMessage()
			if err != nil {
				log.Printf("read: %v", err)
				close(done)
				return
			}

			var msg map[string]interface{}
			if err := json.Unmarshal(raw, &msg); err != nil {
				continue
			}

			msgType, _ := msg["type"].(string)
			prompt, _ := msg["prompt"].(string)
			sessionID, _ := msg["sessionId"].(string)
			if sessionID == "" {
				sessionID = fmt.Sprintf("mock-session-%d", time.Now().Unix())
			}

			log.Printf("← received: type=%s prompt=%q", msgType, truncate(prompt, 50))

			switch msgType {
			case "agent_prompt":
				// Simulate Hermes response as model.* events
				turnID := fmt.Sprintf("mock-turn-%d", time.Now().UnixNano())

				// Turn started
				sendJSON(conn, map[string]interface{}{
					"entity_type": "turn",
					"event_type":  "started",
					"turnId":      turnID,
				})

				// Agent status: thinking
				sendJSON(conn, map[string]interface{}{
					"entity_type": "agent",
					"event_type":  "status",
					"turnId":      turnID,
					"status":      "thinking",
					"label":       "Thinking…",
					"icon":        "🧠",
				})

				time.Sleep(500 * time.Millisecond)

				// Message delta
				sendJSON(conn, map[string]interface{}{
					"entity_type": "message",
					"event_type":  "delta",
					"turnId":      turnID,
					"role":        "assistant",
					"content":     fmt.Sprintf("你好！我收到了你的消息：「%s」\n\n这是一个 mock agent 的回复。实际部署时会由 Hermes 接管。", prompt),
				})

				// Tool call simulation
				sendJSON(conn, map[string]interface{}{
					"entity_type": "tool",
					"event_type":  "call",
					"turnId":      turnID,
					"callId":      "mock-call-1",
					"name":        "read_file",
					"arguments":   `{"path":"/tmp/test.txt"}`,
				})

				// Agent status: done
				sendJSON(conn, map[string]interface{}{
					"entity_type": "agent",
					"event_type":  "status",
					"turnId":      turnID,
					"status":      "done",
					"label":       "Done",
					"icon":        "✅",
				})

				// Turn done
				sendJSON(conn, map[string]interface{}{
					"entity_type": "turn",
					"event_type":  "done",
					"turnId":      turnID,
					"responseId":  turnID,
					"status":      "completed",
				})

				// Back to idle
				sendJSON(conn, map[string]interface{}{
					"entity_type": "agent",
					"event_type":  "status",
					"status":      "idle",
				})

				log.Printf("→ responded to prompt")

			case "agent_stop":
				log.Printf("← stop requested, mode=%v", msg["mode"])
				sendJSON(conn, map[string]interface{}{
					"type":      "agent_cancelled",
					"sessionId": sessionID,
				})

			default:
				log.Printf("← unhandled: %s", msgType)
			}
		}
	}()

	// Heartbeat: send agent_online periodically
	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				// Keepalive is handled by WS ping/pong
			}
		}
	}()

	log.Printf("mock-agent ready — open http://localhost:9879 to chat")
	<-sigCh
	log.Printf("shutting down...")
	conn.Close()
	<-done
}

func sendJSON(conn *websocket.Conn, v interface{}) {
	if err := conn.WriteJSON(v); err != nil {
		log.Printf("write: %v", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
