// colstat-d - system metrics daemon for Quickshell
// Copyright (C) 2026 drewslam
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program. If not, see <https://www.gnu.org/licenses/>.
package main

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Battery status enum
type BatStatus int

const (
	BatUnknown BatStatus = iota
	BatCharging
	BatDischarging
	BatFull
)

// Update interface types
type Update any

type CPUUpdate struct{ Value int }
type RAMUpdate struct{ Value int }
type NetUpdate struct {
	SSID     string
	Strength int
}
type VolUpdate struct {
	Level float64
	Muted bool
}
type MicUpdate struct {
	Level float64
	Muted bool
}
type BatUpdate struct {
	Pct    int
	Status BatStatus
}
type BrightUpdate struct{ Value int }

// State types
type NetState struct {
	SSID     string `json:"ssid"`
	Strength int    `json:"strength"`
}

type VolState struct {
	Level float64 `json:"level"`
	Muted bool    `json:"muted"`
}

type MicState struct {
	Level float64 `json:"level"`
	Muted bool    `json:"muted"`
}

type BatState struct {
	Pct    int       `json:"pct"`
	Status BatStatus `json:"status"`
}

type SystemState struct {
	CPU    int      `json:"cpu"`
	RAM    int      `json:"ram"`
	Net    NetState `json:"net"`
	Vol    VolState `json:"vol"`
	Mic    MicState `json:"mic"`
	Bat    BatState `json:"bat"`
	Bright int      `json:"bright"`
}

// Hub
type Hub struct {
	state      SystemState
	clients    map[net.Conn]bool
	register   chan net.Conn
	unregister chan net.Conn
	updates    chan Update // signal to broadcast current state
}

func NewHub() *Hub {
	return &Hub{
		clients:    make(map[net.Conn]bool),
		register:   make(chan net.Conn),
		unregister: make(chan net.Conn),
		updates:    make(chan Update),
	}
}

func (h *Hub) Run() {
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case conn := <-h.register:
			h.clients[conn] = true

		case conn := <-h.unregister:
			delete(h.clients, conn)
			conn.Close()

		case u := <-h.updates:
			switch v := u.(type) {
			case CPUUpdate:
				h.state.CPU = v.Value
			case RAMUpdate:
				h.state.RAM = v.Value
			case NetUpdate:
				h.state.Net.SSID = v.SSID
				h.state.Net.Strength = v.Strength
			case VolUpdate:
				h.state.Vol.Level = v.Level
				h.state.Vol.Muted = v.Muted
			case MicUpdate:
				h.state.Mic.Level = v.Level
				h.state.Mic.Muted = v.Muted
			case BatUpdate:
				h.state.Bat.Pct = v.Pct
				h.state.Bat.Status = v.Status
			case BrightUpdate:
				h.state.Bright = v.Value
			}

		case <-ticker.C:
			payload, err := json.Marshal(h.state)
			if err != nil {
				log.Print("marshal error:", err)
				continue
			}
			payload = append(payload, '\n')

			for client := range h.clients {
				_, err := client.Write(payload)
				if err != nil {
					go func(c net.Conn) { h.unregister <- c }(client)
				}
			}
		}
	}
}

// Workers
type CPUWorker struct {
	prevUser   int
	prevSystem int
	prevTotal  int
}

func (w *CPUWorker) Run(updates chan Update) {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		if file, err := os.Open("/proc/stat"); err == nil {
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				fields := strings.Fields(scanner.Text())
				if fields[0] != "cpu" {
					continue
				}
				user, _ := strconv.Atoi(fields[1])
				nice, _ := strconv.Atoi(fields[2])
				system, _ := strconv.Atoi(fields[3])
				idle, _ := strconv.Atoi(fields[4])

				total := user + nice + system + idle
				deltaUser := user - w.prevUser
				deltaSystem := system - w.prevSystem
				deltaTotal := total - w.prevTotal

				cpu := 0
				if w.prevTotal > 0 && deltaTotal > 0 {
					cpu = (deltaUser + deltaSystem) * 100 / deltaTotal
				}

				w.prevUser = user
				w.prevSystem = system
				w.prevTotal = total

				file.Close()
				updates <- CPUUpdate{Value: cpu}
				break
			}
		}
	}
}

type RAMWorker struct{}

func (w *RAMWorker) Run(updates chan Update) {
	ticker := time.NewTicker(1 * time.Second)
	for range ticker.C {
		if file, err := os.Open("/proc/meminfo"); err == nil {
			scanner := bufio.NewScanner(file)
			var avail, total int
			for scanner.Scan() {
				str := scanner.Text()
				if strings.Contains(str, "Mem") {
					fields := strings.Fields(str)
					if strings.Contains(fields[0], "Total") {
						total, _ = strconv.Atoi(fields[1])
					} else if strings.Contains(fields[0], "Available") {
						avail, _ = strconv.Atoi(fields[1])
					}
				}
				if total > 0 && avail > 0 {
					break
				}
			}

			used := total - avail
			pct := used * 100 / total

			file.Close()
			updates <- RAMUpdate{Value: pct}
		}
	}
}

type MediaWorker struct{}

func (w *MediaWorker) Run(updates chan Update) {
	ticker := time.NewTicker(2 * time.Second)
	for range ticker.C {
		var outBuf, inBuf, brightBuf strings.Builder
		var outVol, inVol float64
		var outMuted, inMuted bool
		var brightPct int

		outCmd := exec.Command("wpctl", "get-volume", "@DEFAULT_SINK@")
		inCmd := exec.Command("wpctl", "get-volume", "@DEFAULT_SOURCE@")
		brightCmd := exec.Command("brightnessctl", "-m")

		outCmd.Stdout = &outBuf
		if err := outCmd.Run(); err != nil {
			log.Print(err)
		}

		inCmd.Stdout = &inBuf
		if err := inCmd.Run(); err != nil {
			log.Print(err)
		}

		brightCmd.Stdout = &brightBuf
		if err := brightCmd.Run(); err != nil {
			log.Print(err)
		}

		outFields := strings.Fields(outBuf.String())
		if outFields[0] == "Volume:" {
			outVol, _ = strconv.ParseFloat(outFields[1], 64)
		}
		if len(outFields) > 2 && strings.Contains(outFields[2], "MUTED") {
			outMuted = true
		} else {
			outMuted = false
		}

		inFields := strings.Fields(inBuf.String())
		if inFields[0] == "Volume:" {
			inVol, _ = strconv.ParseFloat(inFields[1], 64)
		}
		if len(inFields) > 2 && strings.Contains(inFields[2], "MUTED") {
			inMuted = true
		} else {
			inMuted = false
		}

		brightFields := strings.Split(brightBuf.String(), ",")
		brightPct, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(brightFields[3]), "%"))

		updates <- VolUpdate{Level: outVol, Muted: outMuted}
		updates <- MicUpdate{Level: inVol, Muted: inMuted}
		updates <- BrightUpdate{Value: brightPct}
	}
}

type NetWorker struct{}

func (w *NetWorker) Run(updates chan Update) {
	ticker := time.NewTicker(10 * time.Second)
	for range ticker.C {
		netCmd := exec.Command("nmcli", "-t", "-f", "SSID,SIGNAL,ACTIVE", "device", "wifi")

		var netBuf strings.Builder
		var ssid string
		var strength int

		netCmd.Stdout = &netBuf
		if err := netCmd.Run(); err != nil {
			log.Print(err)
		}

		netList := strings.Split(netBuf.String(), "\n")

		for _, line := range netList {
			fields := strings.Split(line, ":")
			if len(fields) > 2 && fields[2] == "yes" {
				ssid = strings.Join(fields[:len(fields)-2], ":")
				strength, _ = strconv.Atoi(fields[len(fields)-2])
				break
			}
		}

		updates <- NetUpdate{ SSID: ssid, Strength: strength }
	}
}

type BatWorker struct{}

func (w *BatWorker) Run(updates chan Update) {
	ticker := time.NewTicker(30 * time.Second)
	for range ticker.C {
		dat, _ := os.ReadFile("/sys/class/power_supply/BAT0/capacity")
		pct, _ := strconv.Atoi(strings.TrimSpace(string(dat)))

		statDat, _ := os.ReadFile("/sys/class/power_supply/BAT0/status")
		statusStr := strings.TrimSpace(string(statDat))

		status := BatUnknown
		switch statusStr {
		case "Discharging":
			status = BatDischarging
		case "Charging":
			status = BatCharging
		case "Full":
			status = BatFull
		}

		updates <- BatUpdate{Pct: pct, Status: status}
	}
}

func main() {
	socketPath := "/tmp/colstat.sock"
	os.Remove(socketPath)

	hub := NewHub()
	go hub.Run()

	// start workers
	workers := []interface{ Run(chan Update) }{
		&CPUWorker{},
		&RAMWorker{},
		&MediaWorker{},
		&NetWorker{},
		&BatWorker{},
	}
	for _, w := range workers {
		go w.Run(hub.updates)
	}

	l, err := net.Listen("unix", socketPath)
	if err != nil {
		log.Fatal(err)
	}
	defer l.Close()

	log.Printf("colstat-d listening on %s", socketPath)

	for {
		conn, err := l.Accept()
		if err != nil {
			log.Print(err)
			continue
		}
		hub.register <- conn
	}
}
