# colstat-d

A lightweight Go daemon that aggregates system metrics and broadcasts them to connected clients via a Unix Domain Socket. Designed as a backend for Quickshell bar widgets, replacing multiple polling processes with a single efficient service.

## License

This project is licensed under the [GNU General Public License v3.0](LICENSE).

You are free to use, modify, and distribute this software under the terms of the GPLv3. Any derivative works must also be distributed under the same license.

## Architecture

colstat-d uses a worker pool pattern with typed update channels. Each worker owns its own poll ticker and sends strongly-typed updates to a central hub. The hub is the sole owner of system state — no mutexes required.

```
┌─────────────┐     ┌─────────────┐
│  CPU/RAM    │     │    Media    │
│  Worker     │     │   Worker   │
│  (1s tick)  │     │  (2s tick) │
└──────┬──────┘     └──────┬──────┘
       │                   │
       ▼                   ▼
┌─────────────────────────────────┐
│             Hub                 │
│  - Owns SystemState             │
│  - Mutates on Update receipt    │
│  - Broadcasts on 1s ticker      │
└─────────────────┬───────────────┘
                  │ JSON over UDS
       ┌──────────┴──────────┐
       ▼                     ▼
  Client A              Client B
```

### Workers

| Worker  | Interval | Source                          |
|---------|----------|---------------------------------|
| CPU     | 1s       | `/proc/stat` (delta-based)      |
| RAM     | 1s       | `/proc/meminfo`                 |
| Media   | 2s       | `wpctl`, `brightnessctl`        |
| Network | 10s      | `nmcli`                         |
| Battery | 30s      | `/sys/class/power_supply`       |

### The Hub

The hub runs a single select loop handling three cases:

- **`register`** — adds a new UDS client
- **`unregister`** — removes a disconnected client
- **`updates`** — receives a typed update, mutates state immediately
- **broadcast ticker** — serializes current state and writes to all clients

Because the hub is the only goroutine that writes to `SystemState`, no mutex is needed.

### Transport

Unix Domain Socket at `/tmp/colstat.sock`. Each broadcast is a single newline-terminated JSON object. Clients connect and receive the full state every second.

## Schema

```json
{
  "cpu": 12,
  "ram": 45,
  "net": {
    "ssid": "Home_WiFi",
    "strength": 80
  },
  "vol": {
    "level": 0.50,
    "muted": false
  },
  "mic": {
    "level": 0.80,
    "muted": false
  },
  "bat": {
    "pct": 85,
    "status": 2
  },
  "bright": 55
}
```

### Battery Status Codes

| Value | Meaning      |
|-------|--------------|
| 0     | Unknown      |
| 1     | Charging     |
| 2     | Discharging  |
| 3     | Full         |

## Dependencies

- `wpctl` — PipeWire session manager CLI (part of `pipewire-pulse`)
- `brightnessctl` — screen brightness control
- `nmcli` — NetworkManager CLI

## Building

```bash
go build -o colstat-d .
```

## Running

```bash
./colstat-d
```
