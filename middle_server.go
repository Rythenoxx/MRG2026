package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

type AuthRequest struct {
	Type   string `json:"type"`
	Listen string `json:"listen"`
}

// --- CONFIGURATION ---
const (
	BRAIN_ADDR = "18.184.135.220:8080" // <--- ONLY STATIC SETTING REQUIRED
	RELAY_PORT = "5001"                // The port this VPS will listen on
)

// getPublicIP reaches out to external providers to find the VPS's true identity
func getPublicIP() string {
	providers := []string{
		"https://api.ipify.org",
		"https://ifconfig.me/ip",
		"https://ident.me",
	}

	client := http.Client{Timeout: 5 * time.Second}
	for _, url := range providers {
		resp, err := client.Get(url)
		if err != nil {
			continue
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		ip := strings.TrimSpace(string(body))
		if ip != "" {
			return ip
		}
	}
	return "127.0.0.1" // Fallback if totally offline
}

func main() {
	// 1. AUTO-IDENTITY
	myPublicIP := getPublicIP()
	fmt.Printf("[+] BOOT: Detected Public IP: %s\n", myPublicIP)

	// 2. BACKGROUND HEARTBEAT
	go func() {
		for {
			conn, err := net.DialTimeout("tcp", BRAIN_ADDR, 5*time.Second)
			if err == nil {
				// Tell the Brain: "I am a fresh relay at this address"
				json.NewEncoder(conn).Encode(AuthRequest{
					Type:   "middle",
					Listen: myPublicIP + ":" + RELAY_PORT,
				})
				conn.Close()
				time.Sleep(30 * time.Second) // Check in every 30s
			} else {
				fmt.Printf("[!] Connection to Brain failed. Retrying in 10s...\n")
				time.Sleep(10 * time.Second)
			}
		}
	}()

	// 3. THE BRIDGE ENGINE
	fmt.Printf("==========================================\n")
	fmt.Printf("   PLUG-AND-PLAY RELAY: %s:%s\n", myPublicIP, RELAY_PORT)
	fmt.Printf("==========================================\n")

	ln, err := net.Listen("tcp", ":"+RELAY_PORT)
	if err != nil {
		fmt.Printf("[!] FATAL ERROR: %v\n", err)
		return
	}

	for {
		// Wait for Ghost or Operator (Order doesn't matter, first two matched)
		c1, err := ln.Accept()
		if err != nil {
			continue
		}

		c2, err := ln.Accept()
		if err != nil {
			c1.Close()
			continue
		}

		fmt.Printf("[+] Connection Tunneled: %s\n", time.Now().Format("15:04:05"))
		go bridge(c1, c2)
	}
}

func bridge(c1, c2 net.Conn) {
	// A tiny pause ensures the OS has fully established both sockets
	// and cleared any lingering RST packets.
	time.Sleep(100 * time.Millisecond)

	errChan := make(chan error, 2)
	go func() { _, err := io.Copy(c1, c2); errChan <- err }()
	go func() { _, err := io.Copy(c2, c1); errChan <- err }()

	<-errChan
	c1.Close()
	c2.Close()
}
