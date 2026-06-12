// pax-manager is the Fleet Cloud API Gateway — deployed on GCP Cloud Run.
// It routes messages between Dashboard UIs and paxd agents via WebSocket.
//
// Architecture:
//
//	Dashboard (browser)  ──WS──┐
//	                           ├── pax-manager ──WS── paxd (agent)
//	Dashboard (browser)  ──WS──┘
//
// Message flow:
//
//	UI → agent: UI sends agent_prompt → Gateway → agent
//	agent → UI: agent sends model.* events → Gateway → all UIs
//
// For now: single agent, broadcast to all UIs. Multi-agent + auth later.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type conn struct {
	id   string
	role string // "agent" or "ui"
	ws   *websocket.Conn
	send chan []byte
}

type hub struct {
	mu     sync.RWMutex
	agents map[string]*conn
	uis    map[string]*conn
}

func (h *hub) broadcastToUIs(data []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, ui := range h.uis {
		select {
		case ui.send <- data:
		default:
		}
	}
}

func (h *hub) routeToAgent(agentID string, data []byte) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if agentID != "" {
		if a, ok := h.agents[agentID]; ok {
			select {
			case a.send <- data:
				return true
			default:
			}
			return false
		}
	}
	// Broadcast to all agents if no specific agentID
	for _, a := range h.agents {
		select {
		case a.send <- data:
		default:
		}
	}
	return len(h.agents) > 0
}

var h = &hub{
	agents: make(map[string]*conn),
	uis:    make(map[string]*conn),
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9879"
	}

	// Static files (Dashboard)
	fs := http.FileServer(http.Dir("static"))
	http.Handle("/", fs)

	// WebSocket handler
	http.HandleFunc("/ws", handleWS)

	addr := ":" + port
	log.Printf("🚀 pax-manager starting on http://localhost%s", addr)
	log.Printf("   Dashboard: http://localhost%s", addr)
	log.Printf("   WS:        ws://localhost%s/ws", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

func handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("upgrade: %v", err)
		return
	}

	c := &conn{
		id:   r.RemoteAddr,
		ws:   ws,
		send: make(chan []byte, 256),
	}

	// Write pump
	go func() {
		defer ws.Close()
		for data := range c.send {
			if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}()

	// Read first message to determine role
	_, raw, err := ws.ReadMessage()
	if err != nil {
		return
	}

	var msg map[string]interface{}
	if err := json.Unmarshal(raw, &msg); err != nil {
		return
	}

	msgType, _ := msg["type"].(string)

	if msgType == "register" {
		handleAgent(c, msg, raw)
	} else {
		handleUI(c, msg, raw)
	}
}

func handleAgent(c *conn, msg map[string]interface{}, raw []byte) {
	c.role = "agent"
	name, _ := msg["name"].(string)
	agentID := name
	if agentID == "" {
		agentID = c.id
	}

	h.mu.Lock()
	h.agents[agentID] = c
	h.mu.Unlock()

	log.Printf("[agent] registered: %s", name)

	ack, _ := json.Marshal(map[string]string{
		"type":    "registered",
		"agentId": agentID,
	})
	c.send <- ack

	// Message loop: agent → UIs
	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
		h.broadcastToUIs(raw)
	}

	// Disconnect
	h.mu.Lock()
	delete(h.agents, agentID)
	h.mu.Unlock()
	log.Printf("[agent] disconnected: %s", name)
}

func handleUI(c *conn, msg map[string]interface{}, raw []byte) {
	c.role = "ui"

	h.mu.Lock()
	h.uis[c.id] = c
	h.mu.Unlock()

	log.Printf("[ui] connected: %s", c.id)

	// Forward first message to agent
	agentID, _ := msg["agentId"].(string)
	if !h.routeToAgent(agentID, raw) {
		log.Printf("[ui] no agent, dropping: %s", msg["type"])
	}

	// Message loop: UI → agent
	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
		// Re-read agentID in case it changes
		var m map[string]interface{}
		if json.Unmarshal(raw, &m) == nil {
			if aid, ok := m["agentId"].(string); ok {
				h.routeToAgent(aid, raw)
				continue
			}
		}
		h.routeToAgent("", raw)
	}

	// Disconnect
	h.mu.Lock()
	delete(h.uis, c.id)
	h.mu.Unlock()
	log.Printf("[ui] disconnected: %s", c.id)
}
