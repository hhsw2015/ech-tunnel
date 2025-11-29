package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// generateSelfSignedCert 生成自签名证书
func generateSelfSignedCert() (tls.Certificate, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"自签名组织"},
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return tls.Certificate{}, err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return tls.Certificate{}, err
	}
	return cert, nil
}

// runWebSocketServer 运行 WebSocket 服务端
func runWebSocketServer(addr string) {
	u, err := url.Parse(addr)
	if err != nil {
		log.Fatal("无效的 WebSocket 地址:", err)
	}

	path := u.Path
	if path == "" {
		path = "/"
	}

	// 解析多个 CIDR 范围
	var allowedNets []*net.IPNet
	for _, cidr := range strings.Split(cidrs, ",") {
		_, allowedNet, err := net.ParseCIDR(strings.TrimSpace(cidr))
		if err != nil {
			log.Fatalf("无法解析 CIDR: %v", err)
		}
		allowedNets = append(allowedNets, allowedNet)
	}

	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool { return true },
		Subprotocols: func() []string {
			if token == "" {
				return nil
			}
			return []string{token}
		}(),
		ReadBufferSize:  65536, // 增加读缓冲区到64KB
		WriteBufferSize: 65536, // 增加写缓冲区到64KB
	}

	http.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		// 验证来源IP
		clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			log.Printf("无法解析客户端地址: %v", err)
			w.Header().Set("Connection", "close")
			http.Error(w, "Bad Request", http.StatusBadRequest)
			return
		}
		clientIPAddr := net.ParseIP(clientIP)
		allowed := false
		for _, allowedNet := range allowedNets {
			if allowedNet.Contains(clientIPAddr) {
				allowed = true
				break
			}
		}
		if !allowed {
			log.Printf("拒绝访问: IP %s 不在允许的范围内 (%s)", clientIP, cidrs)
			w.Header().Set("Connection", "close")
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		// 验证 Subprotocol token
		if token != "" {
			clientToken := r.Header.Get("Sec-WebSocket-Protocol")
			if clientToken != token {
				log.Printf("Token验证失败，来自 %s", r.RemoteAddr)
				w.Header().Set("Connection", "close")
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		wsConn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Println("WebSocket 升级失败:", err)
			return
		}

		log.Printf("新的 WebSocket 连接来自 %s", r.RemoteAddr)
		go handleWebSocket(wsConn)
	})

	// 启动服务器
	if u.Scheme == "wss" {
		server := &http.Server{
			Addr: u.Host,
		}

		if certFile != "" && keyFile != "" {
			log.Printf("WebSocket 服务端使用提供的TLS证书启动，监听 %s%s", u.Host, path)
			server.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
			log.Fatal(server.ListenAndServeTLS(certFile, keyFile))
		} else {
			cert, err := generateSelfSignedCert()
			if err != nil {
				log.Fatalf("生成自签名证书时出错: %v", err)
			}
			tlsConfig := &tls.Config{
				Certificates: []tls.Certificate{cert},
				MinVersion:   tls.VersionTLS13,
			}
			server.TLSConfig = tlsConfig
			log.Printf("WebSocket 服务端使用自签名证书启动，监听 %s%s", u.Host, path)
			log.Fatal(server.ListenAndServeTLS("", ""))
		}
	} else {
		log.Printf("WebSocket 服务端启动，监听 %s%s", u.Host, path)
		log.Fatal(http.ListenAndServe(u.Host, nil))
	}
}

// handleWebSocket 处理单个 WebSocket 连接
func handleWebSocket(wsConn *websocket.Conn) {
	// 创建一个 context 用于通知所有 goroutine 退出
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 函数退出时取消所有子 goroutine

	var mu sync.Mutex
	var connMu sync.RWMutex
	conns := make(map[string]net.Conn)

	// UDP 连接管理
	udpConns := make(map[string]*net.UDPConn)
	udpTargets := make(map[string]*net.UDPAddr)

	defer func() {
		// 先取消所有 goroutine
		cancel()

		// 关闭所有 TCP 连接（这会让阻塞的 Read 立即返回错误）
		connMu.Lock()
		for id, c := range conns {
			_ = c.Close()
			log.Printf("[服务端] 清理TCP连接: %s", id)
		}
		conns = make(map[string]net.Conn)
		connMu.Unlock()

		// 关闭所有 UDP 连接
		connMu.Lock()
		for id, uc := range udpConns {
			_ = uc.Close()
			log.Printf("[服务端] 清理UDP连接: %s", id)
		}
		udpConns = make(map[string]*net.UDPConn)
		udpTargets = make(map[string]*net.UDPAddr)
		connMu.Unlock()

		// 最后关闭 WebSocket
		_ = wsConn.Close()
		log.Printf("WebSocket 连接 %s 已完全清理", wsConn.RemoteAddr())
	}()

	// 设置WebSocket保活
	wsConn.SetPingHandler(func(message string) error {
		mu.Lock()
		defer mu.Unlock()
		return wsConn.WriteMessage(websocket.PongMessage, []byte(message))
	})

	for {
		typ, msg, readErr := wsConn.ReadMessage()
		if readErr != nil {
			if !isNormalCloseError(readErr) {
				log.Printf("WebSocket 读取失败 %s: %v", wsConn.RemoteAddr(), readErr)
			}
			return // defer 会触发清理
		}

		if typ == websocket.BinaryMessage {
			// 处理 UDP 数据（带 connID）
			if len(msg) > 9 && string(msg[:9]) == "UDP_DATA:" {
				s := string(msg)
				parts := strings.SplitN(s[9:], "|", 2)
				if len(parts) == 2 {
					connID := parts[0]
					data := []byte(parts[1])

					connMu.RLock()
					udpConn, ok1 := udpConns[connID]
					targetAddr, ok2 := udpTargets[connID]
					connMu.RUnlock()
					if ok1 {
						if ok2 {
							if _, err := udpConn.WriteToUDP(data, targetAddr); err != nil {
								log.Printf("[服务端UDP:%s] 发送到目标失败: %v", connID, err)
							} else {
								log.Printf("[服务端UDP:%s] 已发送数据到 %s，大小: %d", connID, targetAddr.String(), len(data))
							}
						}
					}
				}
				continue
			}

			// 支持二进制携带文本前缀 "DATA:" 进行多路复用
			if len(msg) > 5 && string(msg[:5]) == "DATA:" {
				s := string(msg)
				parts := strings.SplitN(s[5:], "|", 2)
				if len(parts) == 2 {
					connID := parts[0]
					payload := parts[1]
					connMu.RLock()
					c, ok := conns[connID]
					connMu.RUnlock()
					if ok {
						if _, err := c.Write([]byte(payload)); err != nil && !isNormalCloseError(err) {
							log.Printf("[服务端] 写入目标失败: %v", err)
						}
					}
				}
				continue
			}
			continue
		}

		data := string(msg)

		// UDP_CONNECT: 建立 UDP 连接（带 connID）
		if strings.HasPrefix(data, "UDP_CONNECT:") {
			parts := strings.SplitN(data[12:], "|", 2)
			if len(parts) == 2 {
				connID := parts[0]
				targetAddr := parts[1]
				log.Printf("[服务端UDP:%s] 收到UDP连接请求，目标: %s", connID, targetAddr)

				udpAddr, err := net.ResolveUDPAddr("udp", targetAddr)
				if err != nil {
					log.Printf("[服务端UDP:%s] 解析目标地址失败: %v", connID, err)
					mu.Lock()
					_ = wsConn.WriteMessage(websocket.TextMessage, []byte("UDP_ERROR:"+connID+"|解析地址失败"))
					mu.Unlock()
					continue
				}

				// 为每个 UDP 连接创建独立的套接字
				udpConn, err := net.ListenUDP("udp", nil)
				if err != nil {
					log.Printf("[服务端UDP:%s] 创建UDP套接字失败: %v", connID, err)
					mu.Lock()
					_ = wsConn.WriteMessage(websocket.TextMessage, []byte("UDP_ERROR:"+connID+"|创建UDP失败"))
					mu.Unlock()
					continue
				}

				connMu.Lock()
				udpConns[connID] = udpConn
				udpTargets[connID] = udpAddr
				connMu.Unlock()

				// 启动 UDP 接收 goroutine（监听 context 取消）
				go func(cID string, uc *net.UDPConn, ctx context.Context) {
					defer func() {
						connMu.Lock()
						delete(udpConns, cID)
						delete(udpTargets, cID)
						connMu.Unlock()
						_ = uc.Close()
					}()

					buffer := make([]byte, 65535)
					for {
						select {
						case <-ctx.Done():
							log.Printf("[服务端UDP:%s] 上下文取消，退出接收循环", cID)
							return
						default:
						}

						// 设置短超时，避免永久阻塞
						_ = uc.SetReadDeadline(time.Now().Add(1 * time.Second))
						n, addr, err := uc.ReadFromUDP(buffer)
						if err != nil {
							if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
								continue // 超时继续循环，检查 ctx
							}
							if !isNormalCloseError(err) {
								log.Printf("[服务端UDP:%s] 读取失败: %v", cID, err)
							}
							return
						}

						log.Printf("[服务端UDP:%s] 收到响应来自 %s，大小: %d", cID, addr.String(), n)

						// 构建响应消息: UDP_DATA:<connID>|<host>:<port>|<data>
						host, portStr, _ := net.SplitHostPort(addr.String())
						response := []byte(fmt.Sprintf("UDP_DATA:%s|%s:%s|", cID, host, portStr))
						response = append(response, buffer[:n]...)

						mu.Lock()
						_ = wsConn.WriteMessage(websocket.BinaryMessage, response)
						mu.Unlock()
					}
				}(connID, udpConn, ctx)

				log.Printf("[服务端UDP:%s] UDP目标已设置: %s", connID, targetAddr)

				// 通知客户端连接成功
				mu.Lock()
				_ = wsConn.WriteMessage(websocket.TextMessage, []byte("UDP_CONNECTED:"+connID))
				mu.Unlock()
			}
			continue
		}

		// UDP_CLOSE: 关闭 UDP 连接
		if strings.HasPrefix(data, "UDP_CLOSE:") {
			connID := strings.TrimPrefix(data, "UDP_CLOSE:")
			connMu.Lock()
			if uc, ok := udpConns[connID]; ok {
				_ = uc.Close()
				delete(udpConns, connID)
				delete(udpTargets, connID)
				log.Printf("[服务端UDP:%s] 连接已关闭", connID)
			}
			connMu.Unlock()
			continue
		}

		// CLAIM: 认领竞选（多通道）
		if strings.HasPrefix(data, "CLAIM:") {
			parts := strings.SplitN(data[6:], "|", 2)
			if len(parts) == 2 {
				connID := parts[0]
				channelID := parts[1]
				mu.Lock()
				_ = wsConn.WriteMessage(websocket.TextMessage, []byte("CLAIM_ACK:"+connID+"|"+channelID))
				mu.Unlock()
			}
			continue
		}

		// TCP: 多路复用建连
		if strings.HasPrefix(data, "TCP:") {
			parts := strings.SplitN(data[4:], "|", 3)
			if len(parts) >= 2 {
				connID := parts[0]
				targetAddr := parts[1]
				var firstFrameData string
				if len(parts) == 3 {
					firstFrameData = parts[2]
				}

				log.Printf("[服务端] 请求TCP转发，连接ID: %s，目标: %s，首帧长度: %d", connID, targetAddr, len(firstFrameData))

				// 启动连接处理 goroutine（传入 ctx）
				go handleTCPConnection(ctx, connID, targetAddr, firstFrameData, wsConn, &mu, &connMu, conns)
			}
			continue
		} else if strings.HasPrefix(data, "DATA:") {
			parts := strings.SplitN(data[5:], "|", 2)
			if len(parts) == 2 {
				id := parts[0]
				payload := parts[1]
				connMu.RLock()
				c, ok := conns[id]
				connMu.RUnlock()
				if ok {
					if _, err := c.Write([]byte(payload)); err != nil && !isNormalCloseError(err) {
						log.Printf("[服务端] 写入目标失败: %v", err)
					}
				}
			}
			continue
		} else if strings.HasPrefix(data, "CLOSE:") {
			id := strings.TrimPrefix(data, "CLOSE:")
			connMu.Lock()
			c, ok := conns[id]
			if ok {
				_ = c.Close()
				delete(conns, id)
				log.Printf("[服务端] 客户端请求关闭连接: %s", id)
			}
			connMu.Unlock()
			continue
		}
	}
}

// handleTCPConnection 处理单个 TCP 连接（独立的函数，监听 context）
func handleTCPConnection(
	ctx context.Context,
	connID, targetAddr, firstFrameData string,
	wsConn *websocket.Conn,
	mu *sync.Mutex,
	connMu *sync.RWMutex,
	conns map[string]net.Conn,
) {
	tcpConn, err := net.Dial("tcp", targetAddr)
	if err != nil {
		log.Printf("[服务端] 连接目标地址 %s 失败: %v", targetAddr, err)
		mu.Lock()
		_ = wsConn.WriteMessage(websocket.TextMessage, []byte("CLOSE:"+connID))
		mu.Unlock()
		return
	}

	// 保存连接
	connMu.Lock()
	conns[connID] = tcpConn
	connMu.Unlock()

	// 确保退出时清理
	defer func() {
		_ = tcpConn.Close()
		connMu.Lock()
		delete(conns, connID)
		connMu.Unlock()
		log.Printf("[服务端] TCP连接已清理: %s", connID)
	}()

	// 发送第一帧
	if firstFrameData != "" {
		if _, err := tcpConn.Write([]byte(firstFrameData)); err != nil {
			log.Printf("[服务端] 发送第一帧失败: %v", err)
			mu.Lock()
			_ = wsConn.WriteMessage(websocket.TextMessage, []byte("CLOSE:"+connID))
			mu.Unlock()
			return
		}
	}

	// 通知客户端连接成功
	mu.Lock()
	_ = wsConn.WriteMessage(websocket.TextMessage, []byte("CONNECTED:"+connID))
	mu.Unlock()

	// 启动读取 goroutine（监听 ctx.Done()）
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32768)
		for {
			select {
			case <-ctx.Done():
				// WebSocket 已关闭，强制关闭 TCP 连接
				log.Printf("[服务端] WebSocket 已关闭，强制关闭 TCP 连接: %s", connID)
				_ = tcpConn.Close()
				return
			default:
			}

			// 设置短超时，避免永久阻塞
			_ = tcpConn.SetReadDeadline(time.Now().Add(1 * time.Second))
			n, err := tcpConn.Read(buf)
			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue // 超时继续循环，检查 ctx
				}
				if !isNormalCloseError(err) {
					log.Printf("[服务端] 从目标读取失败: %v", err)
				}
				mu.Lock()
				_ = wsConn.WriteMessage(websocket.TextMessage, []byte("CLOSE:"+connID))
				mu.Unlock()
				return
			}

			mu.Lock()
			writeErr := wsConn.WriteMessage(websocket.BinaryMessage, append([]byte("DATA:"+connID+"|"), buf[:n]...))
			mu.Unlock()

			if writeErr != nil {
				if !isNormalCloseError(writeErr) {
					log.Printf("[服务端] 写入 WebSocket 失败: %v", writeErr)
				}
				return
			}
		}
	}()

	// 等待读取 goroutine 结束
	<-done
}
