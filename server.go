package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ======================== WebSocket 服务端 ========================

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
		// 性能优化: 增大缓冲区到 1MB
		ReadBufferSize:  1048576, // 1MB
		WriteBufferSize: 1048576, // 1MB
		// 性能优化: 启用压缩以节省带宽(弱网环境)
		EnableCompression: true,
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
		} else if strings.HasPrefix(data, "ACK:") {
			parts := strings.SplitN(data[4:], "|", 2)
			if len(parts) == 2 {
				connID := parts[0]
				var seq int64
				fmt.Sscanf(parts[1], "%d", &seq)

				ackChansMu.RLock()
				ch, ok := ackChans[connID]
				ackChansMu.RUnlock()
				if ok {
					select {
					case ch <- seq:
					default:
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

// ======================== ACK 分发机制 ========================
var (
	ackChansMu sync.RWMutex
	ackChans   = make(map[string]chan int64)
)

// ======================== 独立的 TCP 连接处理函数（监听 context） ========================
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

	// 性能优化: 设置TCP参数
	if tcpConnReal, ok := tcpConn.(*net.TCPConn); ok {
		_ = tcpConnReal.SetNoDelay(true)
		_ = tcpConnReal.SetKeepAlive(true)
		_ = tcpConnReal.SetKeepAlivePeriod(30 * time.Second)
		_ = tcpConnReal.SetReadBuffer(1048576)  // 1MB
		_ = tcpConnReal.SetWriteBuffer(1048576) // 1MB
	}

	// 保存连接
	connMu.Lock()
	conns[connID] = tcpConn
	connMu.Unlock()

	// 初始化拥塞控制器
	controller := NewViolentCongestionController()

	// 注册 ACK 通道
	ackChan := make(chan int64, 1000)
	ackChansMu.Lock()
	ackChans[connID] = ackChan
	ackChansMu.Unlock()

	// 确保退出时清理
	defer func() {
		ackChansMu.Lock()
		delete(ackChans, connID)
		ackChansMu.Unlock()

		_ = tcpConn.Close()
		connMu.Lock()
		delete(conns, connID)
		connMu.Unlock()
		log.Printf("[服务端] TCP连接已清理: %s", connID)
	}()

	// 启动 ACK 消费者
	type packetInfo struct {
		sentTime time.Time
		size     int
	}
	pendingPackets := make(map[int64]packetInfo)
	var pendingMu sync.Mutex

	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case seq, ok := <-ackChan:
				if !ok {
					return
				}
				pendingMu.Lock()
				if info, exists := pendingPackets[seq]; exists {
					delete(pendingPackets, seq)
					pendingMu.Unlock()

					rtt := time.Since(info.sentTime)
					controller.OnAck(info.size, rtt)
				} else {
					pendingMu.Unlock()
				}
			}
		}
	}()

	// 发送第一帧 (不计入拥塞控制，简化处理)
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

		// 集成自适应监控
		monitor := NewAdaptiveMonitor()
		var seq int64 = 0

		for {
			select {
			case <-ctx.Done():
				log.Printf("[服务端] WebSocket 已关闭，强制关闭 TCP 连接: %s", connID)
				_ = tcpConn.Close()
				return
			default:
			}

			_ = tcpConn.SetReadDeadline(time.Now().Add(5 * time.Second))

			// 自适应调整缓冲区大小
			currentBufSize := monitor.GetBufferSize()
			var buf []byte
			var bufPtr *[]byte

			if currentBufSize == 1048576 {
				bufPtr = bufferPool.Get().(*[]byte)
				buf = *bufPtr
			} else {
				buf = make([]byte, currentBufSize)
			}

			n, err := tcpConn.Read(buf)

			// 归还缓冲区
			if bufPtr != nil {
				bufferPool.Put(bufPtr)
			}

			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					continue
				}
				if !isNormalCloseError(err) {
					log.Printf("[服务端] 从目标读取失败: %v", err)
				}
				mu.Lock()
				_ = wsConn.WriteMessage(websocket.TextMessage, []byte("CLOSE:"+connID))
				mu.Unlock()
				return
			}

			monitor.Update(n)

			// === 拥塞控制: 等待窗口 ===
			controller.WaitWindow(n)

			seq++
			currentSeq := seq

			// 构造带序列号的消息: DATA:connID|seq|payload
			header := fmt.Sprintf("DATA:%s|%d|", connID, currentSeq)
			headerBytes := []byte(header)

			message := make([]byte, len(headerBytes)+n)
			copy(message, headerBytes)
			copy(message[len(headerBytes):], buf[:n])

			// 记录发送时间
			pendingMu.Lock()
			pendingPackets[currentSeq] = packetInfo{sentTime: time.Now(), size: n}
			pendingMu.Unlock()

			controller.OnDataSent(n)

			mu.Lock()
			writeErr := wsConn.WriteMessage(websocket.BinaryMessage, message)
			mu.Unlock()

			if writeErr != nil {
				if !isNormalCloseError(writeErr) {
					log.Printf("[服务端] 写入 WebSocket 失败: %v", writeErr)
				}
				return
			}
		}
	}()

	<-done
}
