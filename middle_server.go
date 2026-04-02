package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"time"
)

type AuthRequest struct {
	Type, Listen string
}

func main() {
	if len(os.Args) < 2 {
		return
	}
	port := os.Args[1]

	go func() {
		for {
			conn, err := net.DialTimeout("tcp", "127.0.0.1:8080", 3*time.Second)
			if err == nil {
				json.NewEncoder(conn).Encode(AuthRequest{Type: "middle", Listen: "127.0.0.1:" + port})
				conn.Close()
				time.Sleep(20 * time.Second)
			} else {
				time.Sleep(5 * time.Second)
			}
		}
	}()

	ln, _ := net.Listen("tcp", ":"+port)
	fmt.Printf("[ACTIVE] Relay %s monitoring...\n", port)

	for {
		c1, err := ln.Accept()
		if err != nil {
			continue
		}
		fmt.Printf("[%s] -> Client Connected.\n", port)

		c2, err := ln.Accept()
		if err != nil {
			c1.Close()
			continue
		}
		fmt.Printf("[%s] -> Operator Connected. BRIDGING.\n", port)

		go bridge(c1, c2, port)
	}
}

func bridge(c1, c2 net.Conn, port string) {
	// Remove the strict 10s deadline - let the OS handle the socket life
	errChan := make(chan error, 2)

	go func() {
		_, err := io.Copy(c1, c2)
		errChan <- err
	}()
	go func() {
		_, err := io.Copy(c2, c1)
		errChan <- err
	}()

	// Wait for either side to close
	<-errChan
	c1.Close()
	c2.Close()
	fmt.Printf("[%s] Bridge Closed.\n", port)
}
