package main

import (
	"log"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

type LegacyConn struct {
	id   string
	ws   *websocket.Conn
	send chan []byte
}

type LegacyHub struct {
	mu     sync.RWMutex
	agents map[string]*LegacyConn
}

func NewLegacyHub() *LegacyHub {
	return &LegacyHub{agents: make(map[string]*LegacyConn)}
}

func (h *LegacyHub) add(agentID string, conn *LegacyConn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.agents[agentID] = conn
}

func (h *LegacyHub) remove(agentID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.agents, agentID)
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

func (s *Server) handleAgentWS(w http.ResponseWriter, r *http.Request) {
	apiKey := websocketAPIKey(r)
	if apiKey == "" {
		writeError(w, http.StatusUnauthorized, "missing api key")
		return
	}
	owner, err := s.store.AuthenticateUserAPIKey(r.Context(), hashSecret(apiKey))
	if err != nil {
		writeStoreError(w, err)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("agent websocket upgrade: %v", err)
		return
	}

	agentID := r.URL.Query().Get("agentId")
	if agentID == "" {
		agentID = r.URL.Query().Get("agentKey")
	}
	if agentID == "" {
		agentID = r.RemoteAddr
	}
	connID := owner.UserID + ":" + agentID

	conn := &LegacyConn{
		id:   connID,
		ws:   ws,
		send: make(chan []byte, 256),
	}
	s.legacy.add(connID, conn)
	log.Printf("agent websocket connected: %s owner=%s", agentID, owner.UserID)

	go func() {
		defer ws.Close()
		for data := range conn.send {
			if err := ws.WriteMessage(websocket.TextMessage, data); err != nil {
				return
			}
		}
	}()

	for {
		if _, _, err := ws.ReadMessage(); err != nil {
			break
		}
	}

	s.legacy.remove(connID)
	close(conn.send)
	log.Printf("agent websocket disconnected: %s owner=%s", agentID, owner.UserID)
}
