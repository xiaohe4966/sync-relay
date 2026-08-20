// SquadSync relay — tiny WebSocket fan-out server.
//
// Clients connect with a room code (the same one they use on the LAN).
// When client A sends a Wire.Cmd, the server forwards it to every other
// client in the same room. Wire.State / Wire.Ack / Wire.AppsList replies
// go back the same way.
//
// Build:   go build -o squadsync-relay .
// Run:     ./squadsync-relay            # default :7879
//          ./squadsync-relay -addr :7879
//
// Endpoints
//   GET  /healthz                 → "ok"
//   GET  /v1/ping                  → "pong"
//   WS   /v1/rooms/<room>/ws      → bidirectional, rooms keyed by path
//
// Message format on the WS (just forward raw text — same as the LAN protocol).
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod      = (pongWait * 9) / 10
	maxMessageBytes = 1 << 20 // 1 MiB
)

// A single client connected to a single room.
type client struct {
	hub     *Hub
	conn    *websocket.Conn
	send    chan []byte
	room    string
	device  string
}

func (c *client) readPump() {
	defer c.hub.unregister(c)
	c.conn.SetReadLimit(maxMessageBytes)
	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			if !errors.Is(err, websocket.ErrCloseSent) {
				log.Printf("readPump: room=%s device=%s err=%v", c.room, c.device, err)
			}
			return
		}
		// Fan out to every OTHER client in the same room. The sender
		// already has the message; we don't echo it back to them.
		c.hub.broadcast(c.room, c, data)
	}
}

func (c *client) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		_ = c.conn.Close()
	}()
	for {
		select {
		case msg, ok := <-c.send:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, nil)
				return
			}
			if err := c.conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

// Hub keeps the set of clients per room and serialises broadcasts.
type Hub struct {
	mu      sync.RWMutex
	rooms   map[string]map[*client]struct{}
}

func newHub() *Hub {
	return &Hub{rooms: make(map[string]map[*client]struct{})}
}

func (h *Hub) register(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[c.room]
	if !ok {
		room = make(map[*client]struct{})
		h.rooms[c.room] = room
	}
	room[c] = struct{}{}
	log.Printf("register room=%s device=%s clients=%d", c.room, c.device, len(room))
}

func (h *Hub) unregister(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	room, ok := h.rooms[c.room]
	if !ok {
		return
	}
	if _, ok := room[c]; ok {
		delete(room, c)
		close(c.send)
		if len(room) == 0 {
			delete(h.rooms, c.room)
		}
	}
	log.Printf("unregister room=%s device=%s clients=%d", c.room, c.device, len(room))
}

func (h *Hub) broadcast(roomName string, sender *client, data []byte) {
	h.mu.RLock()
	room, ok := h.rooms[roomName]
	if !ok {
		h.mu.RUnlock()
		return
	}
	peers := make([]*client, 0, len(room))
	for c := range room {
		if c == sender {
			continue
		}
		peers = append(peers, c)
	}
	h.mu.RUnlock()
	for _, c := range peers {
		select {
		case c.send <- data:
		default:
			// Slow consumer: drop. The client will recover on the next
			// heartbeat / reconnect.
			log.Printf("drop: room=%s device=%s (slow consumer)", c.room, c.device)
		}
	}
}

func (h *Hub) stats() (rooms, clients int) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	rooms = len(h.rooms)
	for _, r := range h.rooms {
		clients += len(r)
	}
	return
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     func(r *http.Request) bool { return true },
}

func serveWs(hub *Hub) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Path: /v1/rooms/<room>/ws
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		// Expect: ["v1","rooms","<room>","ws"]
		if len(parts) != 4 || parts[0] != "v1" || parts[1] != "rooms" || parts[3] != "ws" {
			http.Error(w, "expected /v1/rooms/<room>/ws", http.StatusNotFound)
			return
		}
		room := parts[2]
		device := r.URL.Query().Get("device")
		if device == "" {
			device = r.RemoteAddr
		}

		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("upgrade error: %v", err)
			return
		}
		c := &client{
			hub:    hub,
			conn:   conn,
			send:   make(chan []byte, 64),
			room:   room,
			device: device,
		}
		hub.register(c)
		go c.writePump()
		c.readPump()
	}
}

func main() {
	addr := flag.String("addr", ":7879", "listen address, e.g. :7879 or 0.0.0.0:7879")
	flag.Parse()

	hub := newHub()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/v1/ping", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("pong"))
	})
	mux.HandleFunc("/v1/stats", func(w http.ResponseWriter, r *http.Request) {
		rooms, clients := hub.stats()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"rooms":` + itoa(rooms) + `,"clients":` + itoa(clients) + `}`,
		))
	})
	mux.HandleFunc("/v1/rooms/", serveWs(hub))

	srv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// Graceful shutdown
	idle := make(chan struct{})
	go func() {
		sigs := make(chan os.Signal, 1)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		<-sigs
		log.Printf("shutting down...")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		close(idle)
	}()

	log.Printf("squadsync-relay listening on %s", *addr)
	log.Printf("  GET  %s/healthz", *addr)
	log.Printf("  GET  %s/v1/ping", *addr)
	log.Printf("  GET  %s/v1/stats", *addr)
	log.Printf("  WS   %s/v1/rooms/<room>/ws?device=<name>", *addr)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatalf("ListenAndServe: %v", err)
	}
	<-idle
}

// Tiny strconv.Itoa that avoids pulling in "strconv" just for two ints.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	neg := i < 0
	if neg {
		i = -i
	}
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

var _ = path.Join // keep path import even if not used here