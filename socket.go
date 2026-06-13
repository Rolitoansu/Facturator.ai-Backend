package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type Client struct {
	conn   *websocket.Conn
	userID string
}

type SocketEvent struct {
	Type    string                 `json:"type"`
	Payload ReceiptProcessedPayload `json:"payload"`
}

type ReceiptProcessedPayload struct {
	ReceiptID string      `json:"receiptId"`
	Status    string      `json:"status"`
	Updates   interface{} `json:"updates,omitempty"`
}

type Hub struct {
	clients    map[string]map[*Client]bool
	register   chan *Client
	unregister chan *Client
	mu         sync.RWMutex
}

var GlobalHub *Hub

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[string]map[*Client]bool),
		register:   make(chan *Client),
		unregister: make(chan *Client),
	}
}

func (h *Hub) Run() {
	for {
		select {
		case client := <-h.register:
			h.mu.Lock()
			if _, ok := h.clients[client.userID]; !ok {
				h.clients[client.userID] = make(map[*Client]bool)
			}
			h.clients[client.userID][client] = true
			h.mu.Unlock()
			fmt.Printf("WebSocket registered: %s (total: %d)\n", client.userID, len(h.clients[client.userID]))

		case client := <-h.unregister:
			h.mu.Lock()
			if connections, ok := h.clients[client.userID]; ok {
				if _, exists := connections[client]; exists {
					delete(connections, client)
					client.conn.Close()
					if len(connections) == 0 {
						delete(h.clients, client.userID)
					}
					fmt.Printf("WebSocket unregistered: %s\n", client.userID)
				}
			}
			h.mu.Unlock()
		}
	}
}

func (h *Hub) SendToUser(userID string, event SocketEvent) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	connections, ok := h.clients[userID]
	if !ok || len(connections) == 0 {
		fmt.Printf("No active WS connection for user: %s\n", userID)
		return
	}

	msgBytes, err := json.Marshal(event)
	if err != nil {
		fmt.Printf("Error marshaling socket event: %v\n", err)
		return
	}

	for client := range connections {
		err := client.conn.WriteMessage(websocket.TextMessage, msgBytes)
		if err != nil {
			fmt.Printf("Error writing message to client of user %s: %v\n", userID, err)
			go func(c *Client) {
				h.unregister <- c
			}(client)
		}
	}
}

func ServeWs(hub *Hub, w http.ResponseWriter, r *http.Request) {
	userID := r.URL.Query().Get("userId")
	if userID == "" {
		http.Error(w, "userId query parameter is required", http.StatusBadRequest)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		fmt.Printf("Error upgrading connection: %v\n", err)
		return
	}

	client := &Client{
		conn:   conn,
		userID: userID,
	}

	hub.register <- client

	go func() {
		defer func() {
			hub.unregister <- client
		}()

		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				break
			}
		}
	}()
}
