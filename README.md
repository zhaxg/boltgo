# boltgo

High-performance QUIC file transfer CLI tool, written in Go.

boltgo is a Go reimplementation of the [AeroSync](https://github.com/zhaxg/AeroSync) QUIC file transfer protocol — stripped down to QUIC only, with no HTTP/S3/FTP or token auth. Single binary, zero config, wire-compatible with the Rust AeroSync implementation.

This project is a **logic port** from the Rust AeroSync codebase. When implementing or debugging, refer to the [original Rust source](https://github.com/zhaxg/AeroSync) for the canonical protocol logic:

| Boltgo (Go) | AeroSync (Rust) | Description |
|-------------|-----------------|-------------|
| `internal/proto/messages.go` | `aerosync-proto/proto/aerosync/wire/v1.proto` | Wire format definitions |
| `internal/receipt/codec.go` | `src/protocols/quic_receipt.rs` | Length-delimited protobuf framing |
| `internal/receipt/state.go` | `aerosync-domain/src/receipt.rs` | 7-state receipt FSM |
| `internal/quic/client.go` | `src/protocols/quic.rs` | QUIC client (upload/download) |
| `internal/quic/server.go` | `src/core/server.rs` (QUIC section) | QUIC server (accept + receive) |
| `internal/quic/tls.go` | `src/core/tls.rs` + `src/protocols/quic.rs` | TLS certificate generation |

> [中文说明](README.zh-CN.md)

## Features

- **QUIC transport** — `quic-go` based, TLS 1.3 auto-negotiation, ALPN `aerosync/1`
- **Zero-config TLS** — self-signed certificates generated automatically
- **SHA-256 integrity** — sender pre-computes, receiver verifies, mismatch auto-rejects
- **Receipt protocol** — bidirectional confirmation: sender knows when receiver processed the file
- **Protobuf wire format** — byte-compatible with Rust AeroSync (prost ↔ hand-rolled Go codec)
- **Control stream multiplexing** — `0x00` sentinel byte distinguishes control from data streams
- **Single binary** — no external dependencies, `go build` and deploy

## Install

### From source

```bash
git clone https://github.com/yourname/boltgo.git
cd boltgo
go build -o boltgo.exe ./cmd/aerosync    # Windows
go build -o boltgo ./cmd/aerosync        # Linux / macOS
```

### Requirements

- Go ≥ 1.21
- Network access (`go mod tidy` needs to download `quic-go`)

## Quick start

### Receiver (target machine)

```bash
boltgo receive --port 7789 --dir ./downloads
```

Output:

```
2026/09/04 15:00:00 [QUIC server] listening on 0.0.0.0:7789 (receive dir: ./downloads)
```

### Sender (source machine)

```bash
boltgo send ./report.csv 192.168.1.10:7789
```

Output:

```
2026/09/04 15:00:05 [QUIC client] connected to 192.168.1.10:7789
2026/09/04 15:00:05 [QUIC client] computing SHA-256 for './report.csv'...
2026/09/04 15:00:05 [QUIC client] sending 'report.csv' (42000 bytes, sha256=a1b2c3d4...)
2026/09/04 15:00:05 [QUIC client] file 'report.csv' sent successfully (42000 bytes)
```

## CLI reference

### `boltgo send`

```
boltgo send <FILE> <HOST:PORT>
```

| Argument | Description | Default |
|----------|-------------|---------|
| `<FILE>` | Source file path | — |
| `<HOST:PORT>` | Target address | — |

### `boltgo receive`

```
boltgo receive [OPTIONS]
```

| Flag | Description | Default |
|------|-------------|---------|
| `--port` | QUIC listen port | 7789 |
| `--dir` | File save directory | ./received |
| `--bind` | Bind address | 0.0.0.0 |
| `--cert` | TLS certificate file (PEM) | auto-generated |
| `--key` | TLS key file (PEM) | auto-generated |

### `boltgo version`

```
boltgo version
# boltgo 0.1.0-go
```

## Protocol

### Wire format

Each transfer uses three QUIC bidirectional streams:

**Control stream** (sender-initiated):
```
[0x00 sentinel] [varint length] [protobuf ControlFrame{TransferStart{...}}]
```

**Data stream** (sender-initiated):
```
UPLOAD:filename:size:token:receipt_id\n
HASH:sha256\n
<raw file bytes>
```

**Receipt frames** (receiver replies on control stream's recv half):
```
[varint length] [protobuf ReceiptFrame{Received{checksum_ok:true, sha256:"..."}}]
[varint length] [protobuf ReceiptFrame{Acked{}}]
```

### Control stream multiplexing

Receiver dispatches on first byte:
- `0x00` → control stream: parse length-delimited `ControlFrame`
- Other (`U`/`D`) → legacy data stream: parse `UPLOAD:`/`DOWNLOAD:` header

### Receipt protocol (RFC-002)

7-state lifecycle machine:

```
INITIATED → STREAM_OPENED → DATA_TRANSFERRED → STREAM_CLOSED → PROCESSING → COMPLETED
                                                           ↘ FAILED (any error)
```

| Frame | Direction | Purpose |
|-------|-----------|---------|
| `BytesReceived` | receiver → sender | Periodic byte progress |
| `Received` | receiver → sender | Checksum verified, all bytes received |
| `Acked` | receiver → sender | Application-level acknowledgement |
| `Nacked` | receiver → sender | Application-level rejection + reason |
| `Failed` | receiver → sender | Structured error (error code + detail) |

### Interop with AeroSync

boltgo is **byte-level wire-compatible** with Rust AeroSync v0.2+:

```bash
# Rust AeroSync sends → Go boltgo receives
aerosync send ./file.bin 192.168.1.10:7789
boltgo receive --port 7789

# Go boltgo sends → Rust AeroSync receives
boltgo send ./file.bin 192.168.1.10:7789
aerosync receive --quic-port 7789
```

## Testing

### Local loopback

```bash
# Terminal 1: start receiver
go run ./cmd/aerosync receive --port 7789 --dir ./inbox

# Terminal 2: create test file and send
echo "Hello boltgo!" > test.txt
go run ./cmd/aerosync send test.txt 127.0.0.1:7789

# Verify receipt
cat ./inbox/test.txt
# Hello boltgo!
```

### Large file

```bash
dd if=/dev/zero of=bigfile.bin bs=1M count=100

go run ./cmd/aerosync receive --port 7789 --dir ./inbox
go run ./cmd/aerosync send bigfile.bin 127.0.0.1:7789

sha256sum bigfile.bin ./inbox/bigfile.bin
# Both hashes must match
```

### Unit tests

```bash
go test ./...
```

## Project structure

```
boltgo/
├── cmd/aerosync/main.go           # CLI entry point (send / receive / version)
├── internal/
│   ├── proto/messages.go          # Protobuf types + hand-rolled codec
│   ├── receipt/
│   │   ├── codec.go               # Length-delimited protobuf framing
│   │   └── state.go               # 7-state receipt FSM
│   └── quic/
│       ├── tls.go                 # Self-signed cert generation
│       ├── client.go              # QUIC client (upload/download)
│       └── server.go              # QUIC server (accept + receive)
├── proto/aerosync/wire/v1.proto   # Canonical protobuf definition
├── go.mod
└── README.md
```

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/quic-go/quic-go` | QUIC transport |
| Go stdlib | TLS, crypto, IO, CLI |

Protobuf codec is hand-rolled — no `protoc` needed, byte-compatible with Rust `prost` output.

## License

MIT
