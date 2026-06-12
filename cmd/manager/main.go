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
//	agent → UI: agent sends model.* events → Gateway → all UIs (raw pass-through, no envelope)
//
// For now: single agent, broadcast to all UIs. Multi-agent + auth later.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

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

	// WebSocket handlers
	http.HandleFunc("/ws", handleWS)
	http.HandleFunc("/api/agent/ws", handleAgentWS)

	// HTTP handlers (paxd → manager)
	http.HandleFunc("/api/agent/register", handleAgentRegister)
	http.HandleFunc("/api/agent/status", handleAgentStatus)

	// Echo test endpoint
	http.HandleFunc("/api/echo", handleEcho)

	addr := ":" + port
	log.Printf("🚀 pax-manager starting on http://localhost%s", addr)
	log.Printf("   Dashboard:  http://localhost%s", addr)
	log.Printf("   WS (UI):    ws://localhost%s/ws", addr)
	log.Printf("   WS (agent): ws://localhost%s/api/agent/ws", addr)
	log.Printf("   API:        http://localhost%s/api/agent/*", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// ── WS: Role-detection (UI first message decides) ──────────────────

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
		handleAgentLoop(c, msg, raw)
	} else {
		handleUILoop(c, msg, raw)
	}
}

// ── WS: Dedicated agent endpoint (paxd connects here) ─────────────

func handleAgentWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("agent ws upgrade: %v", err)
		return
	}

	agentID := r.URL.Query().Get("key")
	if agentID == "" {
		agentID = r.RemoteAddr
	}

	c := &conn{
		id:   agentID,
		role: "agent",
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

	// Register immediately — paxd is read-only, it won't send a register message
	h.mu.Lock()
	h.agents[agentID] = c
	h.mu.Unlock()

	log.Printf("[agent] connected: %s", agentID)

	// Raw pass-through: every message from agent is broadcast as-is to UIs
	for {
		_, raw, err := ws.ReadMessage()
		if err != nil {
			break
		}
		h.broadcastToUIs(raw)
	}

	h.mu.Lock()
	delete(h.agents, agentID)
	h.mu.Unlock()
	log.Printf("[agent] disconnected: %s", agentID)
}

// ── Agent message loop (raw pass-through, no envelope) ────────────

func handleAgentLoop(c *conn, msg map[string]interface{}, raw []byte) {
	c.role = "agent"
	name, _ := msg["name"].(string)
	agentID := name
	if agentID == "" {
		agentID = c.id
	}

	if _, exists := h.agents[agentID]; !exists {
		h.mu.Lock()
		h.agents[agentID] = c
		h.mu.Unlock()
		log.Printf("[agent] registered: %s", name)
	}

	// Raw pass-through: every message from agent is broadcast as-is
	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
		h.broadcastToUIs(raw)
	}

	h.mu.Lock()
	delete(h.agents, agentID)
	h.mu.Unlock()
	log.Printf("[agent] disconnected: %s", name)
}

// ── UI message loop ────────────────────────────────────────────────

func handleUILoop(c *conn, msg map[string]interface{}, raw []byte) {
	c.role = "ui"

	h.mu.Lock()
	h.uis[c.id] = c
	h.mu.Unlock()

	log.Printf("[ui] connected: %s", c.id)

	// Forward first message to agent (transform agent_prompt → chat)
	routeMsg(msg, raw)

	for {
		_, raw, err := c.ws.ReadMessage()
		if err != nil {
			break
		}
		var m map[string]interface{}
		if json.Unmarshal(raw, &m) == nil {
			routeMsg(m, raw)
			continue
		}
		h.routeToAgent("", raw)
	}

	h.mu.Lock()
	delete(h.uis, c.id)
	h.mu.Unlock()
	log.Printf("[ui] disconnected: %s", c.id)
}

// routeMsg transforms UI message format to what paxd expects, then routes.
// UI → {"type":"agent_prompt","prompt":"hello"}
// paxd expects → {"type":"chat","content":"hello","message_id":"...","session_id":""}
func routeMsg(msg map[string]interface{}, raw []byte) {
	msgType, _ := msg["type"].(string)
	agentID, _ := msg["agentId"].(string)

	if msgType == "agent_prompt" {
		prompt, _ := msg["prompt"].(string)
		sessionID, _ := msg["sessionId"].(string)
		transformed, _ := json.Marshal(map[string]interface{}{
			"type":        "chat",
			"content":     prompt,
			"message_id":  generateMsgID(),
			"session_id":  sessionID,
			"agent_id":    agentID,
		})
		h.routeToAgent(agentID, transformed)
	} else {
		h.routeToAgent(agentID, raw)
	}
}

var msgCounter int

func generateMsgID() string {
	msgCounter++
	return fmt.Sprintf("msg-%d", msgCounter)
}

// ── HTTP: Agent Registration ───────────────────────────────────────

func handleAgentRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Name     string `json:"name"`
		Hostname string `json:"hostname"`
		Platform string `json:"platform"`
		Version  string `json:"version"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if req.Name == "" {
		req.Name = req.Hostname
	}
	resp := map[string]interface{}{
		"agent_id": req.Name,
		"api_key":  "key-" + req.Name,
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
	log.Printf("[api] agent registered: %s", req.Name)
}

// ── HTTP: Echo (test/debug) ────────────────────────────────────────

func handleEcho(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}

	var payload json.RawMessage
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}

	// Wrap input as a request struct, echo it back
	resp := map[string]interface{}{
		"ok": true,
		"request": map[string]interface{}{
			"type":    "echo",
			"payload": payload,
		},
		"timestamp": time.Now().UTC().Format(time.RFC3339),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
	log.Printf("[api] echo: %s", truncate(string(payload), 80))
}

// ── HTTP: Agent Status ─────────────────────────────────────────────

func handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	// Accept status reports, no-op for now
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
