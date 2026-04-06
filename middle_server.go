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
const PSK = "8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE" // Must match in all components

func handleHandshake(conn net.Conn) bool {
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, len(PSK))

	_, err := io.ReadFull(conn, buf)
	if err != nil || string(buf) != PSK {
		fmt.Printf("[!] Handshake Failed from %s\n", conn.RemoteAddr())
		conn.Write([]byte("ERROR: UNAUTHORIZED\n"))
		conn.Close()
		return false
	}

	// Handshake success - clear deadline for the bridge
	conn.SetReadDeadline(time.Time{})
	return true
}

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
	myPublicIP := getPublicIP()

	// Heartbeat to Brain
	go func() {
		for {
			conn, err := net.DialTimeout("tcp", BRAIN_ADDR, 5*time.Second)
			if err == nil {
				json.NewEncoder(conn).Encode(AuthRequest{
					Type:   "middle",
					Listen: myPublicIP + ":" + RELAY_PORT,
				})
				conn.Close()
				time.Sleep(30 * time.Second)
			} else {
				time.Sleep(10 * time.Second)
			}
		}
	}()

	ln, _ := net.Listen("tcp", ":"+RELAY_PORT)
	fmt.Printf("[+] Relay active on %s:%s (PSK Protected)\n", myPublicIP, RELAY_PORT)

	for {
		c1, _ := ln.Accept()
		if !handleHandshake(c1) {
			continue
		}

		c2, _ := ln.Accept()
		if !handleHandshake(c2) {
			c1.Close()
			continue
		}

		fmt.Printf("[+] Secure Bridge Established: %s\n", time.Now().Format("15:04:05"))
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
