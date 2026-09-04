# boltgo

基于 QUIC 协议的高性能文件传输 CLI 工具，Go 语言实现。

**robocopy 替代方案** — 专为内网 445 端口被封、SMB 不可用的环境设计。boltgo 使用 QUIC（端口 7789）跨防火墙传输文件，无需特殊网络配置。

基于 [AeroSync](https://github.com/zhaxg/AeroSync) QUIC 协议精简版，单二进制部署，零配置开箱即用，与 Rust AeroSync 字节级兼容。

## 为什么需要 boltgo？

很多企业和政府网络中，SMB（端口 445）被防火墙策略阻断。这导致 `robocopy`、`cp`、共享文件夹全部不可用。boltgo 解决这个问题：

- 使用 **QUIC**（基于 UDP），通常能穿越阻断 TCP 445 的防火墙
- **无需服务端部署** — 接收方只需运行 `boltgo receive`，发送方直连
- **单文件** — 无需安装、无依赖、无需管理员权限
- **目录传输** — 默认递归，保持目录结构
- **智能去重** — 比较 SHA-256，自动跳过相同文件

```bash
# 替代: robocopy \\server\share\project C:\local\project /MIR
# boltgo 等效:
boltgo receive --save-to C:\local\project --port 7789
boltgo send D:\server\share\project 192.168.1.10:7789
```

## 特性

- **QUIC 传输**：基于 `quic-go`，TLS 1.3 自动协商，ALPN `aerosync/1`
- **零配置 TLS**：自动生成自签名证书
- **智能去重**：比较 SHA-256，跳过相同文件，覆盖不同文件
- **目录传输**：默认递归，保持目录结构
- **远程路径**：发送时指定接收端子路径
- **小文件优化**：256KB 以下走快速路径，无 receipt 开销
- **大文件优化**：并发传输，边传边算 SHA-256，缓冲写入
- **Receipt 协议**：与 AeroSync 双向确认
- **Protobuf 线格式**：与 Rust AeroSync 字节级兼容
- **Flag 任意顺序**：参数放在命令行任意位置都能识别

## 安装

### 从源码编译

```bash
git clone https://github.com/yourname/boltgo.git
cd boltgo
go build -o boltgo.exe ./cmd/boltgo    # Windows
go build -o boltgo ./cmd/boltgo        # Linux / macOS
```

### 前置要求

- Go ≥ 1.21
- 网络可达（`go mod tidy` 需下载 `quic-go` 依赖）

## 快速开始

### 接收端

```bash
boltgo receive --port 7789 --save-to ./downloads
```

### 发送端 — 单文件

```bash
boltgo send ./report.csv 192.168.1.10:7789
```

### 发送端 — 目录（递归，保持结构）

```bash
boltgo send ./project 192.168.1.10:7789
```

### 发送端 — 带远程路径

```bash
boltgo send ./data.bin 192.168.1.10:7789 /backups/2024
```

### 重复发送（智能去重 — 相同文件跳过）

```bash
boltgo send ./project 192.168.1.10:7789
# 第一次：传输所有文件
# 第二次：跳过相同文件，只传变化的
```

## CLI 参考

### 全局参数

| 参数 | 说明 |
|------|------|
| `-v, --verbose` | 详细日志输出（含文件名+行号） |

### `boltgo send`

```
boltgo send [flags] <file|dir> <host:port> [remote-path]
```

| 参数 | 说明 | 默认值 |
|------|------|--------|
| `<file\|dir>` | 源文件或目录（自动递归） | — |
| `<host:port>` | 目标地址 | — |
| `[remote-path]` | 追加到接收端 `--save-to` 的子路径 | 根目录 |

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--no-verify` | false | 跳过接收端 SHA-256 完整性校验 |
| `--parallel` | 5 | 大文件最大并发传输数 |
| `--retry` | 3 | 每个文件重试次数 |
| `--small-threshold` | 262144 (256KB) | 小于此大小走快速路径 |

### `boltgo receive`

```
boltgo receive [flags]
```

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--port` | 7789 | QUIC 监听端口 |
| `--save-to` | ./received | 文件保存目录 |
| `--bind` | 0.0.0.0 | 绑定地址 |

### `boltgo version`

```bash
boltgo version
# boltgo 0.1.0-go
```

## 协议说明

### 线格式

每次传输使用两条 QUIC 双向流：

**控制流**（发送方发起）：
```
[0x00 哨兵] [varint 长度] [protobuf ControlFrame{TransferStart{...}}]
```

**数据流**（发送方发起）：
```
UPLOAD:filename:size:token:receipt_id\n
HASH:sha256\n
<原始文件字节>
```

**Receipt 帧**（接收方回复到控制流）：
```
[varint 长度] [protobuf ReceiptFrame{Received{checksum_ok:true, sha256:"..."}}]
[varint 长度] [protobuf ReceiptFrame{Acked{}}]
```

### 智能去重

接收方收到文件时：
1. 比较文件大小 — 不同 → 覆盖
2. 大小相同 → 计算现有文件 SHA-256
3. SHA-256 匹配 → SKIP（日志：`SKIP 'file': identical`）
4. SHA-256 不同 → 覆盖

### 与 AeroSync 互通

boltgo 与 Rust AeroSync v0.2+ **字节级线格式兼容**：

```bash
# Rust AeroSync 发送 → Go boltgo 接收
aerosync send ./file.bin 192.168.1.10:7789
boltgo receive --port 7789

# Go boltgo 发送 → Rust AeroSync 接收
boltgo send ./file.bin 192.168.1.10:7789
aerosync receive --quic-port 7789
```

## 测试验证

### 本地回环 — 单文件

```bash
go run ./cmd/boltgo receive --port 7789 --save-to ./inbox
echo "Hello boltgo!" > test.txt
go run ./cmd/boltgo send test.txt 127.0.0.1:7789
cat ./inbox/test.txt
```

### 本地回环 — 目录

```bash
go run ./cmd/boltgo receive --port 7789 --save-to ./inbox
go run ./cmd/boltgo send ./mydir 127.0.0.1:7789
find ./inbox/mydir -type f
```

### 智能去重

```bash
go run ./cmd/boltgo receive --port 7789 --save-to ./inbox
go run ./cmd/boltgo send ./mydir 127.0.0.1:7789    # 传输所有
go run ./cmd/boltgo send ./mydir 127.0.0.1:7789    # 跳过相同
# 服务端日志: SKIP 'file': identical (sha256=...)
```

### 性能调优

```bash
# 调整小文件阈值
go run ./cmd/boltgo send --small-threshold 1048576 ./dir host:7789

# 增加并发
go run ./cmd/boltgo send --parallel 10 ./dir host:7789

# 跳过校验
go run ./cmd/boltgo send --no-verify ./dir host:7789
```

### Go 单元测试

```bash
go test ./...
```

## 项目结构

```
boltgo/
├── cmd/boltgo/
│   ├── main.go        # CLI 入口（send / receive / version）
│   ├── proto.go       # Protobuf 类型 + 手写编解码器
│   ├── receipt.go     # Length-delimited protobuf 帧编解码
│   └── quic.go        # QUIC 客户端 + 服务端 + TLS
├── bin/
│   ├── boltgo         # Linux
│   └── boltgo.exe     # Windows（UPX 压缩）
├── proto/aerosync/wire/v1.proto
├── go.mod
├── go.sum
└── README.md
```

## 依赖

| 包 | 用途 |
|---|------|
| `github.com/quic-go/quic-go` | QUIC 传输层 |
| Go 标准库 | TLS、加密、IO、CLI |

Protobuf 编解码器为手写实现，无需 `protoc` 代码生成，与 Rust `prost` 输出字节级兼容。

## License

MIT
