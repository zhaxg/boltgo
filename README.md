# boltgo

High-performance QUIC file transfer CLI tool, written in Go.

**A drop-in replacement for robocopy** — designed for environments where SMB (port 445) is blocked and network drives are unavailable. boltgo uses QUIC (port 7879) to transfer files across firewalls without any special network configuration.

Built on the [AeroSync](https://github.com/TechVerseOdyssey/AeroSync) QUIC protocol — wire-compatible with the Rust AeroSync implementation. **2x faster than AeroSync** (176 MB/s vs 87 MB/s in benchmarks).

> [中文说明](README.zh-CN.md)

## Why boltgo?

In many corporate and government networks, SMB (port 445) is blocked by firewall policy. This makes `robocopy`, `cp`, and shared folders unusable. boltgo solves this by:

- Using **QUIC** (UDP-based) which typically passes through firewalls that block TCP 445
- **No server setup required** — receiver just runs `boltgo receive`, sender connects directly
- **Single binary** — no installation, no dependencies, no admin rights needed
- **Directory transfer** — recursive by default, preserves structure
- **Smart dedup** — compares SHA-256, skips identical files automatically
- **2x faster than AeroSync** — optimized QUIC implementation

```bash
# Replaces: robocopy \\server\share\project C:\local\project /MIR
# boltgo equivalent:
boltgo receive --save-to C:\local\project --port 7879
boltgo send D:\server\share\project 192.168.1.10:7879
```

## Features

- **QUIC transport** — `quic-go` based, TLS 1.3 auto-negotiation, ALPN `aerosync/1`
- **Zero-config TLS** — self-signed certificates generated automatically
- **Smart dedup** — compares SHA-256, skips identical files, overwrites different files
- **Directory transfer** — recursive by default, structure preserved
- **Remote path** — specify destination subpath on receiver
- **High concurrency** — 10 parallel transfers (configurable)
- **Receipt protocol** — bidirectional confirmation with AeroSync
- **Protobuf wire format** — byte-compatible with Rust AeroSync (prost ↔ hand-rolled Go codec)
- **Any-order flags** — flags can be placed anywhere in the command
- **Per-file logging** — shows each file being sent with SHA-256 hash

## Install

### From source

```bash
git clone https://github.com/yourname/boltgo.git
cd boltgo
go build -o boltgo.exe ./cmd/boltgo    # Windows
go build -o boltgo ./cmd/boltgo        # Linux / macOS
```

### Requirements

- Go ≥ 1.21
- Network access (`go mod tidy` needs to download `quic-go`)

## Quick start

### Receiver (target machine)

```bash
boltgo receive --port 7879 --save-to ./downloads
```

### Sender — single file

```bash
boltgo send ./report.csv 192.168.1.10:7879
```

### Sender — directory (recursive, structure preserved)

```bash
boltgo send ./project 192.168.1.10:7879
```

### Sender — with remote path

```bash
boltgo send ./data.bin 192.168.1.10:7879 /backups/2024
```

### Re-run (smart dedup — identical files skipped)

```bash
boltgo send ./project 192.168.1.10:7789
# First run: transfers all files
# Second run: skips identical files, only transfers changed ones
```

## CLI reference

### Global flags

| Flag | Description |
|------|-------------|
| `-v, --verbose` | Verbose log output. Shows protocol details:<br>- Control stream events<br>- Receipt frames<br>- SHA-256 hashes<br>- Stream open/close<br>- Server-side transfer progress<br><br>Default mode only shows essential info (connected, sent, completed). |

### `boltgo send`

```
boltgo send [flags] <file|dir> <host:port> [remote-path]
```

| Argument | Description | Default |
|----------|-------------|---------|
| `<file\|dir>` | Source file or directory (auto-recursive) | — |
| `<host:port>` | Target address | — |
| `[remote-path]` | Subpath appended to receiver's `--save-to` | root |

| Flag | Default | Description |
|------|---------|-------------|
| `--no-verify` | false | Skip SHA-256 integrity check on receiver |
| `--parallel` | 5 | Max concurrent transfers for large files |
| `--retry` | 3 | Retry attempts per file |
| `--small-threshold` | 262144 (256KB) | Files below this use fast path (no receipt) |

### `boltgo receive`

```
boltgo receive [flags]
```

| Flag | Default | Description |
|------|---------|-------------|
| `--port` | 7789 | QUIC listen port |
| `--save-to` | ./received | Directory to save received files |
| `--bind` | 0.0.0.0 | Bind address |

### `boltgo version`

```
boltgo version
# boltgo 0.1.0-go
```

### `boltgo probe`

Query the server's save-to directory.

```
boltgo probe <host:port>
```

**Example:**

```bash
$ boltgo probe 192.168.1.10:7879
D:\hgmes\tmp
```

**Use case in Azure Pipelines:**

```powershell
$saveDir = boltgo probe 192.168.1.10:7879
Write-Host "Server save-to: $saveDir"
boltgo send ./output $saveDir
```

## Protocol

### Wire format

Each transfer uses two QUIC bidirectional streams per file:

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

**Receipt frames** (receiver replies on control stream):
```
[varint length] [protobuf ReceiptFrame{Received{checksum_ok:true, sha256:"..."}}]
[varint length] [protobuf ReceiptFrame{Acked{}}]
```

### Smart dedup

When receiver gets a file that already exists:
1. Compare file sizes — different → overwrite
2. Sizes match → compute SHA-256 of existing file
3. SHA-256 matches → SKIP (log: `SKIP 'file': identical`)
4. SHA-256 differs → overwrite

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

### Local loopback — single file

```bash
go run ./cmd/boltgo receive --port 7789 --save-to ./inbox
echo "Hello boltgo!" > test.txt
go run ./cmd/boltgo send test.txt 127.0.0.1:7789
cat ./inbox/test.txt
```

### Local loopback — directory

```bash
go run ./cmd/boltgo receive --port 7789 --save-to ./inbox
go run ./cmd/boltgo send ./mydir 127.0.0.1:7789
find ./inbox/mydir -type f
```

### Smart dedup

```bash
go run ./cmd/boltgo receive --port 7789 --save-to ./inbox
go run ./cmd/boltgo send ./mydir 127.0.0.1:7789    # transfers all
go run ./cmd/boltgo send ./mydir 127.0.0.1:7789    # skips identical
# Server log: SKIP 'file': identical (sha256=...)
```

### Performance tuning

```bash
# Tune small file threshold
boltgo send --small-threshold 1048576 ./dir host:7879

# Increase concurrency
boltgo send --parallel 10 ./dir host:7879

# Skip integrity check
boltgo send --no-verify ./dir host:7879
```

### Unit tests

```bash
go test ./...
```

## Project structure

```
boltgo/
├── cmd/boltgo/
│   ├── main.go        # CLI entry (send / receive / version)
│   ├── proto.go       # Protobuf types + hand-rolled codec
│   ├── receipt.go     # Length-delimited protobuf framing
│   └── quic.go        # QUIC client + server + TLS
├── bin/
│   ├── boltgo         # Linux
│   └── boltgo.exe     # Windows (UPX compressed)
├── go.mod
├── go.sum
└── README.md
```

## Performance

| Metric | boltgo | AeroSync |
|--------|--------|----------|
| Protocol | QUIC | QUIC |
| 125 files / 449 MB | **2.55s** | 5.16s |
| Speed | **176 MB/s** | 87 MB/s |
| Improvement | **2x faster** | baseline |

Tested on: Linux (210) → Linux (86) over LAN

## Exit codes

Inspired by robocopy, boltgo returns meaningful exit codes:

| Code | Meaning | Description |
|------|---------|-------------|
| 0 | Success | All files transferred successfully |
| 1 | Success (copied) | Files were copied |
| 2 | Success (skipped) | Files were skipped (dedup) |
| 3 | Success (mixed) | Some copied, some skipped |
| 8 | Connection error | Cannot reach server |
| 9 | TLS error | Handshake failed |
| 10 | Partial failure | Some files failed |
| 11 | All failed | All files failed to send |
| 16 | Fatal error | Bad arguments, path not found |

### Azure Pipelines example

```powershell
boltgo send ./data 192.168.1.10:7879
if ($LASTEXITCODE -ge 8) {
    Write-Error "boltgo failed! Exit code: $LASTEXITCODE"
    exit 1
}
Write-Host "boltgo succeeded! Exit code: $LASTEXITCODE" -ForegroundColor Green
```

### Bash example

```bash
boltgo send ./data 192.168.1.10:7879
if [ $? -ge 8 ]; then
    echo "boltgo failed with exit code $?"
    exit 1
fi
echo "boltgo succeeded"
```

## Verbose logging example

### Default mode
```
[boltgo-c] connected to 192.168.1.10:7879
[boltgo-c] sent 'data/file1.dll' (1048576 bytes, sha256=abc123def456)
[boltgo-c] sent 'data/file2.dll' (2097152 bytes, sha256=789012345678)
[boltgo-c] completed: 2/2 files, 3.00 MB in 0.15s (20.0 MB/s)
```

### Verbose mode (`-v`)
```
[boltgo-c] connected to 192.168.1.10:7879
[boltgo-c] sent 'data/file1.dll' (1048576 bytes, sha256=abc123def456)
[boltgo-c] sent 'data/file2.dll' (2097152 bytes, sha256=789012345678)
[boltgo-c] completed: 2/2 files, 3.00 MB in 0.15s (20.0 MB/s)

# Server side (with -v):
[boltgo-s] listening on 0.0.0.0:7879
[boltgo-s] new connection from 192.168.1.10:12345
[boltgo-s] control: TransferStart receipt=abc123 file=data/file1.dll size=1048576
[boltgo-s] received 'data/file1.dll' 1048576 bytes
[boltgo-s] sent Received + Acked for receipt=abc123
```

## Dependencies

| Package | Purpose |
|---------|---------|
| `github.com/quic-go/quic-go` | QUIC transport |
| Go stdlib | TLS, crypto, IO, CLI |

Protobuf codec is hand-rolled — no `protoc` needed, byte-compatible with Rust `prost` output.

## License

MIT
