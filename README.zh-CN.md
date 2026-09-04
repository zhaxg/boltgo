# boltgo

基于 QUIC 协议的高性能文件传输 CLI 工具，Go 语言实现。

boltgo 是 [AeroSync](https://github.com/zhaxg/AeroSync) 的 Go 语言精简版，仅保留 QUIC 传输协议，去掉 HTTP / S3 / FTP / Token 认证等模块。单二进制部署，零配置开箱即用，两端互通兼容。

本项目是 Rust AeroSync 代码库的**逻辑移植**。实现或调试时，参考[原始 Rust 源码](https://github.com/zhaxg/AeroSync)获取协议的权威逻辑：

| Boltgo (Go) | AeroSync (Rust) | 说明 |
|-------------|-----------------|------|
| `internal/proto/messages.go` | `aerosync-proto/proto/aerosync/wire/v1.proto` | 线格式定义 |
| `internal/receipt/codec.go` | `src/protocols/quic_receipt.rs` | Length-delimited protobuf 帧编解码 |
| `internal/receipt/state.go` | `aerosync-domain/src/receipt.rs` | 7 状态 Receipt 状态机 |
| `internal/quic/client.go` | `src/protocols/quic.rs` | QUIC 客户端（上传/下载） |
| `internal/quic/server.go` | `src/core/server.rs` (QUIC 部分) | QUIC 服务端（监听 + 文件接收） |
| `internal/quic/tls.go` | `src/core/tls.rs` + `src/protocols/quic.rs` | TLS 证书生成 |

## 特性

- **QUIC 传输**：基于 `quic-go`，TLS 1.3 自动协商，ALPN `aerosync/1`
- **零配置 TLS**：自动生成自签名证书，开发环境即插即用
- **SHA-256 完整性校验**：发送方预计算，接收方验证，不一致自动拒绝
- **Receipt 协议**：双向确认机制，发送方知道接收方是否成功处理文件
- **Protobuf 线格式**：与 Rust AeroSync 实现字节级兼容
- **控制流复用**：`0x00` 哨兵字节区分控制流和数据流，无需版本升级
- **单二进制**：无外部依赖，`go build` 即可部署

## 安装

### 从源码编译

```bash
git clone https://github.com/yourname/boltgo.git
cd boltgo
go build -o boltgo.exe ./cmd/aerosync    # Windows
go build -o boltgo ./cmd/aerosync        # Linux / macOS
```

### 前置要求

- Go ≥ 1.21
- 网络可达（`go mod tidy` 需下载 `quic-go` 依赖）

## 快速开始

### 接收端（目标机器）

```bash
boltgo receive --port 7789 --dir ./downloads
```

输出：

```
2026/09/04 15:00:00 [QUIC server] listening on 0.0.0.0:7789 (receive dir: ./downloads)
```

### 发送端（源机器）

```bash
# 发送单个文件
boltgo send ./report.csv 192.168.1.10:7789

# 发送大文件
boltgo send ./dataset.tar.gz 192.168.1.10:7789
```

输出：

```
2026/09/04 15:00:05 [QUIC client] connected to 192.168.1.10:7789
2026/09/04 15:00:05 [QUIC client] computing SHA-256 for './report.csv'...
2026/09/04 15:00:05 [QUIC client] sending 'report.csv' (42000 bytes, sha256=a1b2c3d4...)
2026/09/04 15:00:05 [QUIC client] file 'report.csv' sent successfully (42000 bytes)
```

接收端输出：

```
2026/09/04 15:00:05 [QUIC server] new connection from 192.168.1.5:52431
2026/09/04 15:00:05 [QUIC server] control: TransferStart receipt=... file=report.csv size=42000
2026/09/04 15:00:05 [QUIC server] receiving 'report.csv' (42000 bytes) → ./downloads/report.csv
2026/09/04 15:00:05 [QUIC server] received 'report.csv' 42000 bytes, sha256=a1b2c3d4...
2026/09/04 15:00:05 [QUIC server] file 'report.csv' saved successfully
```

## CLI 参考

### `boltgo send`

```
boltgo send <FILE> <HOST:PORT>
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `<FILE>` | 源文件路径 | — |
| `<HOST:PORT>` | 目标地址 | — |

### `boltgo receive`

```
boltgo receive [OPTIONS]
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `--port` | QUIC 监听端口 | 7789 |
| `--dir` | 文件保存目录 | ./received |
| `--bind` | 绑定地址 | 0.0.0.0 |
| `--cert` | TLS 证书文件（PEM） | 自动生成 |
| `--key` | TLS 私钥文件（PEM） | 自动生成 |

### `boltgo version`

```bash
boltgo version
# boltgo 0.1.0-go
```

## 协议说明

### QUIC 传输层

boltgo 使用 QUIC 协议（基于 `quic-go` 库），ALPN 标识为 `aerosync/1`。TLS 1.3 自动协商，开发模式跳过证书验证。

### 线格式

每次传输使用三条 QUIC 双向流：

**控制流**（发送方发起）：
```
[0x00 sentinel] [varint 长度] [protobuf ControlFrame{TransferStart{...}}]
```

**数据流**（发送方发起）：
```
UPLOAD:filename:size:token:receipt_id\n
HASH:sha256\n
<原始文件字节>
```

**Receipt 帧**（接收方回传，走控制流的接收半边）：
```
[varint 长度] [protobuf ReceiptFrame{Received{checksum_ok:true, sha256:"..."}}]
[varint 长度] [protobuf ReceiptFrame{Acked{}}]
```

### 控制流复用

接收方通过首字节分发：

- `0x00` → 控制流：解析 length-delimited `ControlFrame`
- 其它字符（`U`/`D`）→ 传统数据流：解析 `UPLOAD:`/`DOWNLOAD:` 头

### Receipt 状态机（RFC-002）

每次传输携带双向 Receipt 流，实现 7 状态生命周期：

```
                ┌──────────────┐
                │  INITIATED   │ ← 创建 Receipt 对象
                └──────┬───────┘
                       │ stream opened
                       ▼
                ┌──────────────┐
            ┌──►│STREAM_OPENED │
            │   └──────┬───────┘
            │          │ all bytes flushed
            │          ▼
            │   ┌──────────────────┐
            │   │DATA_TRANSFERRED  │
            │   └──────┬───────────┘
            │          │ stream closed
            │          ▼
            │   ┌──────────────┐
            │   │STREAM_CLOSED │
            │   └──────┬───────┘
            │          │ checksum verified
            │          ▼
            │   ┌──────────────┐
            │   │  PROCESSING  │
            │   └──────┬───────┘
            │          │ app ack
            │          ▼
            │   ┌──────────────┐
            │   │  COMPLETED   │ ← 终态：成功
            │   └──────────────┘
            │
            │   任何错误 / nack
            └─►┌──────────────┐
               │    FAILED    │ ← 终态：失败
               └──────────────┘
```

Receipt 帧类型：

| 帧类型 | 方向 | 说明 |
|--------|------|------|
| `BytesReceived` | 接收方 → 发送方 | 周期性字节进度 |
| `Received` | 接收方 → 发送方 | 校验通过，所有字节已接收 |
| `Acked` | 接收方 → 发送方 | 应用层确认（agent ack） |
| `Nacked` | 接收方 → 发送方 | 应用层拒绝 + 原因 |
| `Failed` | 接收方 → 发送方 | 结构化错误（错误码 + 详情） |

### 与 AeroSync 互通

boltgo 与 Rust AeroSync v0.2+ **字节级线格式兼容**：

- 相同 ALPN `aerosync/1`
- 相同 `0x00` 控制流哨兵
- 相同 length-delimited protobuf 编码（`prost` ↔ 手写 Go codec）
- 相同 `UPLOAD:` 数据流头格式
- 相同 Receipt 帧结构

```bash
# Rust AeroSync 发送 → Go boltgo 接收
aerosync send ./file.bin 192.168.1.10:7789
boltgo receive --port 7789

# Go boltgo 发送 → Rust AeroSync 接收
boltgo send ./file.bin 192.168.1.10:7789
aerosync receive --quic-port 7789
```

## 测试验证

### 本地回环测试

```bash
# 终端 1：启动接收端
go run ./cmd/aerosync receive --port 7789 --dir ./inbox

# 终端 2：创建测试文件并发送
echo "Hello boltgo!" > test.txt
go run ./cmd/aerosync send test.txt 127.0.0.1:7789

# 验证接收
cat ./inbox/test.txt
# Hello boltgo!
```

### 大文件测试

```bash
# 生成 100MB 测试文件
dd if=/dev/zero of=bigfile.bin bs=1M count=100

# 终端 1
go run ./cmd/aerosync receive --port 7789 --dir ./inbox

# 终端 2
go run ./cmd/aerosync send bigfile.bin 127.0.0.1:7789

# 校验
sha256sum bigfile.bin ./inbox/bigfile.bin
# 两个值应该完全一致
```

### SHA-256 完整性测试

```bash
# 发送带 hash 的文件
sha256sum test.txt
# 输出: a1b2c3... test.txt

# 接收端日志会显示：
# [QUIC server] received 'test.txt' 14 bytes, sha256=a1b2c3...
# 如果 hash 不匹配，传输会失败并输出：
# [QUIC server] HASH MISMATCH: expected=xxx actual=yyy
```

### Go 单元测试

```bash
go test ./...
```

## 项目结构

```
boltgo/
├── cmd/aerosync/main.go           # CLI 入口（send / receive / version）
├── internal/
│   ├── proto/messages.go          # Protobuf 消息类型 + 手写编解码器
│   ├── receipt/
│   │   ├── codec.go               # Length-delimited protobuf 帧编解码
│   │   └── state.go               # 7 状态 Receipt 状态机
│   └── quic/
│       ├── tls.go                 # 自签名证书生成
│       ├── client.go              # QUIC 客户端（上传/下载）
│       └── server.go              # QUIC 服务端（监听 + 文件接收）
├── proto/aerosync/wire/v1.proto   # Protobuf 协议定义（单文件真相源）
├── go.mod
├── go.sum
└── README.md
```

## 与 AeroSync 的差异

| 特性 | AeroSync (Rust) | boltgo (Go) |
|------|-----------------|-------------|
| 语言 | Rust 1.89+ | Go 1.21+ |
| QUIC 库 | quinn 0.11 | quic-go |
| 传输协议 | QUIC + HTTP + S3 + FTP | 仅 QUIC |
| Token 认证 | HMAC-SHA256 | 无（已移除） |
| 断点续传 | ✓（32MB 分片） | 后续版本支持 |
| MCP 工具 | 11 个 | 无 |
| Python SDK | ✓ | 无 |
| mDNS 发现 | ✓ | 无 |
| Receipt 协议 | ✓ | ✓（兼容） |
| Protobuf 编解码 | prost（代码生成） | 手写（零依赖） |
| 二进制大小 | ~15 MB | ~8 MB |

## 依赖

| 包 | 用途 |
|---|------|
| `github.com/quic-go/quic-go` | QUIC 传输层 |
| Go 标准库 | TLS、加密、IO、CLI |

Protobuf 编解码器为手写实现，无需 `protoc` 代码生成，与 Rust `prost` 输出字节级兼容。

## License

MIT
