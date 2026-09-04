package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/quic-go/quic-go"
)

// ──────────────────────────────────────────────────────────────────
// TLS (same as AeroSync: self-signed, ALPN aerosync/1)
// ──────────────────────────────────────────────────────────────────

func generateSelfSignedCert() (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"boltgo"}},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{"localhost"},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyDER, _ := x509.MarshalECPrivateKey(key)
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return tls.X509KeyPair(certPEM, keyPEM)
}

func serverTLSConfig() (*tls.Config, error) {
	cert, err := generateSelfSignedCert()
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"aerosync/1"},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func clientTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"aerosync/1"},
		MinVersion:         tls.VersionTLS13,
	}
}

// ──────────────────────────────────────────────────────────────────
// Client Config
// ──────────────────────────────────────────────────────────────────

type ClientConfig struct {
	ServerAddr     string
	DevMode        bool
	NoVerify       bool
	MaxConcurrent  int
	RetryAttempts  int
	SmallThreshold uint64
}

type fileInfo struct {
	path       string
	remoteName string
	size       uint64
}

// ──────────────────────────────────────────────────────────────────
// SendDir - AeroSync compatible protocol
// ──────────────────────────────────────────────────────────────────

func SendDir(ctx context.Context, cfg ClientConfig, dirPath, remotePrefix string) error {
	conn, err := dialServer(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.CloseWithError(0, "done")

	start := time.Now()

	// Collect all files
	var files []fileInfo
	var totalSize uint64
	parentDir := filepath.Dir(dirPath)

	err = filepath.Walk(dirPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		relPath, e := filepath.Rel(parentDir, path)
		if e != nil {
			relPath = filepath.Base(path)
		}
		remoteName := filepath.ToSlash(relPath)
		if remotePrefix != "" {
			remoteName = remotePrefix + "/" + remoteName
		}
		totalSize += uint64(info.Size())
		files = append(files, fileInfo{path: path, remoteName: remoteName, size: uint64(info.Size())})
		return nil
	})
	if err != nil {
		return err
	}

	// Concurrency control - like AeroSync's Semaphore
	maxConc := cfg.MaxConcurrent
	if maxConc <= 0 {
		maxConc = 10
	}
	sem := make(chan struct{}, maxConc)

	var wg sync.WaitGroup
	var sentCount int64
	var sentBytes int64
	var errCount int64

	// Send all files concurrently with AeroSync protocol
	for _, fi := range files {
		wg.Add(1)
		sem <- struct{}{} // acquire semaphore
		go func(f fileInfo) {
			defer wg.Done()
			defer func() { <-sem }() // release semaphore

			if e := sendFileAeroSync(ctx, conn, f, cfg.NoVerify); e != nil {
				atomic.AddInt64(&errCount, 1)
				log.Printf("[QUIC client] ERROR sending '%s': %v", f.remoteName, e)
			} else {
				atomic.AddInt64(&sentCount, 1)
				atomic.AddInt64(&sentBytes, int64(f.size))
			}
		}(fi)
	}

	wg.Wait()

	// Grace period for server to finish writing
	time.Sleep(500 * time.Millisecond)

	elapsed := time.Since(start).Round(time.Millisecond)
	speed := float64(0)
	if elapsed.Seconds() > 0 {
		speed = float64(sentBytes) / elapsed.Seconds() / 1048576
	}
	log.Printf("[QUIC client] completed: %d/%d files, %s in %s (%.1f MB/s)",
		sentCount, len(files), formatBytes(totalSize), elapsed, speed)

	if errCount > 0 {
		return fmt.Errorf("%d files failed to send", errCount)
	}
	return nil
}

// sendFileAeroSync sends one file using AeroSync's QUIC protocol:
// 1. Open control stream → send TransferStart (protobuf)
// 2. Open data stream → send UPLOAD header + file data
// 3. Drainer reads receipt in background (non-blocking)
func sendFileAeroSync(ctx context.Context, conn *quic.Conn, fi fileInfo, noVerify bool) error {
	// Pre-compute SHA-256 for dedup (like AeroSync)
	f, err := os.Open(fi.path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("hash: %w", err)
	}
	sha256hex := fmt.Sprintf("%x", h.Sum(nil))
	f.Seek(0, io.SeekStart)

	fi_size := uint64(fi.size)
	receiptID := fmt.Sprintf("%x", time.Now().UnixNano()) // unique receipt ID

	// ── 1. Control stream (like AeroSync) ──
	ctrlSend, err := conn.OpenStream()
	if err != nil {
		return fmt.Errorf("open control stream: %w", err)
	}

	// Send TransferStart protobuf (length-delimited)
	ts := &TransferStart{
		ReceiptID: receiptID,
		FileName:  fi.remoteName,
		SizeBytes: fi_size,
		Sha256:    sha256hex,
		Metadata: &Metadata{
			ID:         receiptID,
			FileName:   fi.remoteName,
			SizeBytes:  fi_size,
			Protocol:   "aerosync/1",
			SessionID:  receiptID,
		},
	}
	cf := &ControlFrame{TransferStart: ts}
	cfBytes := encodeControlFrame(cf)

	// Write sentinel (0x00) + length-delimited ControlFrame in ONE write
	ctrlBuf := make([]byte, 0, 1+len(cfBytes))
	ctrlBuf = append(ctrlBuf, 0x00) // CONTROL_STREAM_SENTINEL
	ctrlBuf = append(ctrlBuf, cfBytes...)
	if _, err := ctrlSend.Write(ctrlBuf); err != nil {
		return fmt.Errorf("write control prelude: %w", err)
	}

	// Spawn receipt drainer (non-blocking, like AeroSync's tokio::spawn)
	receiptDone := make(chan struct{})
	go drainReceipts(ctrlSend, receiptDone)

	// ── 2. Data stream ──
	// Non-blocking OpenStream with retry (handle stream limits)
	var dataStream *quic.Stream
	for i := 0; i < 50; i++ {
		dataStream, err = conn.OpenStream()
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("open data stream: %w", err)
	}

	// Send UPLOAD header (like AeroSync: UPLOAD:filename:size:token:receipt_id\n)
	token := "" // no auth token for now
	header := fmt.Sprintf("UPLOAD:%s:%d:%s:%s\n", fi.remoteName, fi_size, token, receiptID)

	// Add HASH line for dedup (like AeroSync)
	if !noVerify {
		header += fmt.Sprintf("HASH:%s\n", sha256hex)
	}

	if _, err := dataStream.Write([]byte(header)); err != nil {
		return fmt.Errorf("write upload header: %w", err)
	}

	// Stream file data
	if _, err := io.Copy(dataStream, f); err != nil {
		return fmt.Errorf("send data: %w", err)
	}

	// Finish the stream (queue FIN)
	dataStream.Close()

	log.Printf("[QUIC client] sent '%s' (%d bytes, sha256=%s)", fi.remoteName, fi_size, sha256hex[:12])

	// ── 3. Best-effort receipt read (non-blocking, like AeroSync) ──
	// Don't block on receipt - the drainer runs in background
	// and the receipt is best-effort per RFC-002 §7.
	select {
	case <-receiptDone:
		// Receipt received quickly
	default:
		// Don't wait - move on to next file
	}

	return nil
}

// drainReceipts reads receipt frames from control stream (like AeroSync)
func drainReceipts(ctrlStream *quic.Stream, done chan<- struct{}) {
	defer close(done)
	for {
		rf, err := decodeReceiptFrame(ctrlStream)
		if err != nil {
			return
		}
		// Check for terminal frames
		if rf.Acked != nil || rf.Nacked != nil || rf.Failed != nil {
			return
		}
		// Received frame is intermediate, keep reading
	}
}

func formatBytes(b uint64) string {
	const (
		KB = 1024
		MB = 1024 * KB
		GB = 1024 * MB
	)
	switch {
	case b >= GB:
		return fmt.Sprintf("%.2f GB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2f MB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.2f KB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// ──────────────────────────────────────────────────────────────────
// Single file send (for sendFile command)
// ──────────────────────────────────────────────────────────────────

func SendFile(ctx context.Context, cfg ClientConfig, filePath, remoteName string) error {
	conn, err := dialServer(ctx, cfg)
	if err != nil {
		return err
	}
	defer conn.CloseWithError(0, "done")

	start := time.Now()
	fi, err := os.Stat(filePath)
	if err != nil {
		return err
	}
	f := fileInfo{path: filePath, remoteName: remoteName, size: uint64(fi.Size())}
	if err := sendFileAeroSync(ctx, conn, f, cfg.NoVerify); err != nil {
		return err
	}
	log.Printf("[QUIC client] completed: 1 file in %s", time.Since(start).Round(time.Millisecond))
	return nil
}

// ──────────────────────────────────────────────────────────────────
// Client Connection
// ──────────────────────────────────────────────────────────────────

func dialServer(ctx context.Context, cfg ClientConfig) (*quic.Conn, error) {
	addr := cfg.ServerAddr
	if !strings.Contains(addr, ":") {
		addr += ":7879"
	}
	conn, err := quic.DialAddr(ctx, addr, clientTLSConfig(), &quic.Config{
		MaxIdleTimeout:       60 * time.Second,
		KeepAlivePeriod:      5 * time.Second,
		MaxIncomingStreams:    512,
		MaxIncomingUniStreams: 512,
	})
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	log.Printf("[QUIC client] connected to %s", addr)
	return conn, nil
}

// ──────────────────────────────────────────────────────────────────
// Server
// ──────────────────────────────────────────────────────────────────

type ServerConfig struct {
	BindAddr    string
	Port        int
	ReceiveDir  string
	MaxFileSize uint64
}

func RunServer(ctx context.Context, cfg ServerConfig) error {
	if err := os.MkdirAll(cfg.ReceiveDir, 0755); err != nil {
		return fmt.Errorf("create receive dir: %w", err)
	}
	tlsCfg, err := serverTLSConfig()
	if err != nil {
		return fmt.Errorf("TLS config: %w", err)
	}
	addr := fmt.Sprintf("%s:%d", cfg.BindAddr, cfg.Port)
	listener, err := quic.ListenAddr(addr, tlsCfg, &quic.Config{
		MaxIncomingStreams:    512,
		MaxIncomingUniStreams: 512,
	})
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer listener.Close()
	log.Printf("[QUIC server] listening on %s (receive dir: %s)", addr, cfg.ReceiveDir)

	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("[QUIC server] accept error: %v", err)
			continue
		}
		log.Printf("[QUIC server] new connection from %s", conn.RemoteAddr())
		go handleConn(ctx, conn, cfg)
	}
}

// ──────────────────────────────────────────────────────────────────
// Server Connection Handler (AeroSync compatible)
// ──────────────────────────────────────────────────────────────────

type quicControlEntry struct {
	metadata *TransferStart
	send     *quic.Stream
}

type quicConnState struct {
	mu     sync.Mutex
	notify chan struct{}
	pend   map[string]quicControlEntry
}

func newQuicConnState() *quicConnState {
	return &quicConnState{notify: make(chan struct{}, 1), pend: make(map[string]quicControlEntry)}
}

func (s *quicConnState) put(rid string, entry quicControlEntry) {
	s.mu.Lock()
	s.pend[rid] = entry
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

func (s *quicConnState) take(rid string) (quicControlEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.pend[rid]
	if ok {
		delete(s.pend, rid)
	}
	return e, ok
}

func (s *quicConnState) waitForEntry(rid string, maxWait time.Duration) (quicControlEntry, bool) {
	deadline := time.Now().Add(maxWait)
	for {
		if e, ok := s.take(rid); ok {
			return e, true
		}
		if time.Now().After(deadline) {
			return quicControlEntry{}, false
		}
		select {
		case <-s.notify:
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func handleConn(ctx context.Context, conn *quic.Conn, cfg ServerConfig) {
	defer conn.CloseWithError(0, "done")
	state := newQuicConnState()

	for {
		stream, err := conn.AcceptStream(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("[QUIC server] stream accept error: %v", err)
			return
		}

		// Read first byte to determine stream type (like AeroSync)
		var firstByte [1]byte
		if _, err := io.ReadFull(stream, firstByte[:]); err != nil {
			log.Printf("[QUIC server] read first byte: %v", err)
			return
		}

		switch firstByte[0] {
		case 0x00:
			// Control stream (AeroSync protocol)
			go handleControlStream(stream, state)
		case 'U', 'D':
			// Data stream (UPLOAD/DOWNLOAD)
			go handleDataStream(stream, cfg.ReceiveDir, state, firstByte[0], cfg.MaxFileSize)
		default:
			log.Printf("[QUIC server] unknown sentinel 0x%02x", firstByte[0])
			io.Copy(io.Discard, stream)
			stream.Close()
		}
	}
}

// handleControlStream processes AeroSync control stream (TransferStart)
func handleControlStream(stream *quic.Stream, state *quicConnState) {
	// Read length-delimited ControlFrame
	cf, err := decodeControlFrame(stream)
	if err != nil {
		log.Printf("[QUIC server] control stream error: %v", err)
		stream.Close()
		return
	}
	if cf.TransferStart == nil {
		log.Printf("[QUIC server] control frame has no TransferStart")
		stream.Close()
		return
	}
	ts := cf.TransferStart
	log.Printf("[QUIC server] control: TransferStart receipt=%s file=%s size=%d",
		ts.ReceiptID, ts.FileName, ts.SizeBytes)
	state.put(ts.ReceiptID, quicControlEntry{metadata: ts, send: stream})
}

// handleDataStream processes UPLOAD/DOWNLOAD data stream
func handleDataStream(stream *quic.Stream, receiveDir string, state *quicConnState, sentinel byte, maxFileSize uint64) {
	// Read UPLOAD header line (with sentinel prepended)
	hdr := make([]byte, 0, 4096)
	hdr = append(hdr, sentinel)
	tmp := make([]byte, 1)
	for {
		if _, err := io.ReadFull(stream, tmp); err != nil {
			stream.Close()
			return
		}
		hdr = append(hdr, tmp[0])
		if tmp[0] == '\n' {
			break
		}
	}
	headerLine := strings.TrimRight(string(hdr), "\n\r")

	if !strings.HasPrefix(headerLine, "UPLOAD:") {
		log.Printf("[QUIC server] bad header: %q", headerLine)
		stream.Close()
		return
	}

	// Parse UPLOAD:filename:size:token:receipt_id
	parts := strings.SplitN(headerLine[7:], ":", 5)
	if len(parts) < 3 {
		log.Printf("[QUIC server] malformed UPLOAD header")
		stream.Close()
		return
	}
	fileName := parts[0]
	fileSize, _ := strconv.ParseUint(parts[1], 10, 64)
	receiptID := ""
	if len(parts) > 3 {
		receiptID = strings.TrimSpace(parts[3])
	}

	// Peek next line: could be HASH: or start of file data
	peekBuf := make([]byte, 0, 256)
	expectedHash := ""
	isHash := false
	for {
		if _, err := io.ReadFull(stream, tmp); err != nil {
			break
		}
		if tmp[0] == '\n' {
			break
		}
		peekBuf = append(peekBuf, tmp[0])
		if len(peekBuf) == 5 && string(peekBuf) == "HASH:" {
			isHash = true
		}
	}
	if isHash {
		expectedHash = string(peekBuf[5:])
	}

	// Handle remaining data
	var dataReader io.Reader
	if !isHash && len(peekBuf) > 0 {
		dataReader = io.MultiReader(bytes.NewReader(peekBuf), stream)
	} else {
		dataReader = stream
	}

	if maxFileSize > 0 && fileSize > maxFileSize {
		log.Printf("[QUIC server] REJECTED '%s': size %d exceeds max", fileName, fileSize)
		io.Copy(io.Discard, dataReader)
		stream.Close()
		return
	}

	destPath := filepath.Join(receiveDir, fileName)

	// Smart dedup: if file exists and hash matches, skip
	if existingFile, err := os.Stat(destPath); err == nil {
		if existingFile.Size() == int64(fileSize) && expectedHash != "" && expectedHash != "pending" {
			ef, err := os.Open(destPath)
			if err == nil {
				h := sha256.New()
				io.Copy(h, ef)
				ef.Close()
				existingHash := fmt.Sprintf("%x", h.Sum(nil))
				if existingHash == expectedHash {
					log.Printf("[QUIC server] SKIP '%s' (hash match)", fileName)
					io.Copy(io.Discard, dataReader)
					stream.Close()
					return
				}
			}
		}
	}

	// Write file
	if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
		log.Printf("[QUIC server] mkdir error: %v", err)
		stream.Close()
		return
	}
	destFile, err := os.Create(destPath)
	if err != nil {
		log.Printf("[QUIC server] create file error: %v", err)
		stream.Close()
		return
	}

	// Buffered write + SHA-256 in-flight
	h := sha256.New()
	bufWriter := bufio.NewWriterSize(destFile, 256*1024)
	tee := io.TeeReader(dataReader, h)
	written, err := io.Copy(bufWriter, tee)
	bufWriter.Flush()
	destFile.Close()
	actualHash := fmt.Sprintf("%x", h.Sum(nil))

	if err != nil {
		log.Printf("[QUIC server] receive error: %v", err)
		os.Remove(destPath)
		stream.Close()
		return
	}

	log.Printf("[QUIC server] received '%s' %d bytes", fileName, written)

	checksumOK := true
	if expectedHash != "" && expectedHash != "pending" && actualHash != expectedHash {
		log.Printf("[QUIC server] HASH MISMATCH: expected=%s actual=%s", expectedHash[:12], actualHash[:12])
		os.Remove(destPath)
		checksumOK = false
	}

	// Send receipt on control stream (AeroSync protocol)
	if receiptID != "" {
		if entry, found := state.waitForEntry(receiptID, 2*time.Second); found {
			sendReceiptFrames(entry.send, receiptID, actualHash, checksumOK)
		}
	}

	// Send SUCCESS on data stream (for compatibility)
	_, _ = stream.Write([]byte("SUCCESS"))
	stream.Close()
}

// sendReceiptFrames sends Received + Acked on control stream
func sendReceiptFrames(ctrlStream *quic.Stream, receiptID, sha256hex string, checksumOK bool) {
	received := &ReceiptFrame{
		ReceiptID: receiptID,
		Received:  &Received{ChecksumOK: checksumOK, Sha256: sha256hex},
	}
	if _, err := ctrlStream.Write(encodeReceiptFrame(received)); err != nil {
		log.Printf("[QUIC server] write Received frame error: %v", err)
		return
	}
	acked := &ReceiptFrame{ReceiptID: receiptID, Acked: &Acked{}}
	if _, err := ctrlStream.Write(encodeReceiptFrame(acked)); err != nil {
		log.Printf("[QUIC server] write Acked frame error: %v", err)
		return
	}
	log.Printf("[QUIC server] sent Received + Acked for receipt=%s", receiptID)
}
