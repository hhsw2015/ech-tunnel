package main

import (
	"bytes"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// ECHPool 多通道客户端连接池
type ECHPool struct {
	wsServerAddr  string
	connectionNum int

	wsConns   []*websocket.Conn
	wsMutexes []sync.Mutex

	mu               sync.RWMutex
	tcpMap           map[string]net.Conn
	udpMap           map[string]*UDPAssociation
	channelMap       map[string]int
	connInfo         map[string]struct{ targetAddr, firstFrameData string }
	claimTimes       map[string]map[int]time.Time
	connected        map[string]chan bool
	boundByChannel   map[int]string
	pendingByChannel map[int]string
}

// NewECHPool 创建新的连接池
func NewECHPool(wsServerAddr string, n int) *ECHPool {
	return &ECHPool{
		wsServerAddr:     wsServerAddr,
		connectionNum:    n,
		wsConns:          make([]*websocket.Conn, n),
		wsMutexes:        make([]sync.Mutex, n),
		tcpMap:           make(map[string]net.Conn),
		udpMap:           make(map[string]*UDPAssociation),
		channelMap:       make(map[string]int),
		connInfo:         make(map[string]struct{ targetAddr, firstFrameData string }),
		claimTimes:       make(map[string]map[int]time.Time),
		connected:        make(map[string]chan bool),
		boundByChannel:   make(map[int]string),
		pendingByChannel: make(map[int]string),
	}
}

// Start 启动连接池的所有连接
func (p *ECHPool) Start() {
	for i := 0; i < p.connectionNum; i++ {
		go p.dialOnce(i)
	}
}

// dialOnce 为指定通道建立连接
func (p *ECHPool) dialOnce(index int) {
	for {
		wsConn, err := dialWebSocketWithECH(p.wsServerAddr, 2)
		if err != nil {
			log.Printf("[客户端] 通道 %d WebSocket(ECH) 连接失败: %v，2秒后重试", index, err)
			time.Sleep(2 * time.Second)
			continue
		}
		p.wsConns[index] = wsConn
		log.Printf("[客户端] 通道 %d WebSocket(ECH) 已连接", index)
		go p.handleChannel(index, wsConn)
		return
	}
}

// RegisterAndClaim 注册一个本地TCP连接，并对所有通道发起认领
func (p *ECHPool) RegisterAndClaim(connID, target, firstFrame string, tcpConn net.Conn) {
	p.mu.Lock()
	p.tcpMap[connID] = tcpConn
	p.connInfo[connID] = struct{ targetAddr, firstFrameData string }{targetAddr: target, firstFrameData: firstFrame}
	if p.claimTimes[connID] == nil {
		p.claimTimes[connID] = make(map[int]time.Time)
	}
	if _, ok := p.connected[connID]; !ok {
		p.connected[connID] = make(chan bool, 1)
	}
	p.mu.Unlock()

	for i, ws := range p.wsConns {
		if ws == nil {
			continue
		}
		p.mu.Lock()
		p.claimTimes[connID][i] = time.Now()
		p.mu.Unlock()
		p.wsMutexes[i].Lock()
		err := ws.WriteMessage(websocket.TextMessage, []byte("CLAIM:"+connID+"|"+fmt.Sprintf("%d", i)))
		p.wsMutexes[i].Unlock()
		if err != nil {
			log.Printf("[客户端] 通道 %d 发送CLAIM失败: %v", i, err)
		}
	}
}

// RegisterUDP 注册UDP关联
func (p *ECHPool) RegisterUDP(connID string, assoc *UDPAssociation) {
	p.mu.Lock()
	p.udpMap[connID] = assoc
	if _, ok := p.connected[connID]; !ok {
		p.connected[connID] = make(chan bool, 1)
	}
	p.mu.Unlock()
}

// SendUDPConnect 发送UDP连接请求（选择第一个可用通道）
func (p *ECHPool) SendUDPConnect(connID, target string) error {
	p.mu.RLock()
	var ws *websocket.Conn
	var chID int
	for i, w := range p.wsConns {
		if w != nil {
			ws = w
			chID = i
			break
		}
	}
	p.mu.RUnlock()

	if ws == nil {
		return fmt.Errorf("没有可用的 WebSocket 连接")
	}

	// 记录通道映射
	p.mu.Lock()
	p.channelMap[connID] = chID
	p.boundByChannel[chID] = connID
	p.mu.Unlock()

	p.wsMutexes[chID].Lock()
	err := ws.WriteMessage(websocket.TextMessage, []byte("UDP_CONNECT:"+connID+"|"+target))
	p.wsMutexes[chID].Unlock()

	return err
}

// SendUDPData 发送UDP数据
func (p *ECHPool) SendUDPData(connID string, data []byte) error {
	p.mu.RLock()
	chID, ok := p.channelMap[connID]
	var ws *websocket.Conn
	if ok && chID < len(p.wsConns) {
		ws = p.wsConns[chID]
	}
	p.mu.RUnlock()

	if !ok || ws == nil {
		return fmt.Errorf("未分配通道")
	}

	msg := append([]byte("UDP_DATA:"+connID+"|"), data...)
	p.wsMutexes[chID].Lock()
	err := ws.WriteMessage(websocket.BinaryMessage, msg)
	p.wsMutexes[chID].Unlock()

	return err
}

// SendUDPClose 关闭UDP连接
func (p *ECHPool) SendUDPClose(connID string) error {
	p.mu.RLock()
	chID, ok := p.channelMap[connID]
	var ws *websocket.Conn
	if ok && chID < len(p.wsConns) {
		ws = p.wsConns[chID]
	}
	p.mu.RUnlock()

	if !ok || ws == nil {
		return nil
	}

	p.wsMutexes[chID].Lock()
	err := ws.WriteMessage(websocket.TextMessage, []byte("UDP_CLOSE:"+connID))
	p.wsMutexes[chID].Unlock()

	// 清理映射
	p.mu.Lock()
	delete(p.channelMap, connID)
	delete(p.boundByChannel, chID)
	delete(p.udpMap, connID)
	p.mu.Unlock()

	return err
}

// WaitConnected 等待连接建立
func (p *ECHPool) WaitConnected(connID string, timeout time.Duration) bool {
	p.mu.RLock()
	ch := p.connected[connID]
	p.mu.RUnlock()
	if ch == nil {
		return false
	}
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// handleChannel 处理单个通道的消息
func (p *ECHPool) handleChannel(channelID int, wsConn *websocket.Conn) {
	wsConn.SetPingHandler(func(message string) error {
		p.wsMutexes[channelID].Lock()
		err := wsConn.WriteMessage(websocket.PongMessage, []byte(message))
		p.wsMutexes[channelID].Unlock()
		return err
	})

	go func() {
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for range t.C {
			p.wsMutexes[channelID].Lock()
			_ = wsConn.WriteMessage(websocket.PingMessage, nil)
			p.wsMutexes[channelID].Unlock()
		}
	}()

	for {
		mt, msg, err := wsConn.ReadMessage()
		if err != nil {
			log.Printf("[客户端] 通道 %d WebSocket读取失败: %v", channelID, err)
			// 重连通道
			p.redialChannel(channelID)
			return
		}

		if mt == websocket.BinaryMessage {
			// 处理 UDP 数据响应: UDP_DATA:<connID>|<host>:<port>|<data>
			if len(msg) > 9 && string(msg[:9]) == "UDP_DATA:" {
				parts := bytes.SplitN(msg[9:], []byte("|"), 3)
				if len(parts) == 3 {
					addrData := string(parts[1])
					data := parts[2]

					p.mu.RLock()
					assoc := p.udpMap[string(parts[0])]
					p.mu.RUnlock()

					if assoc != nil {
						assoc.handleUDPResponse(addrData, data)
					}
				}
				continue
			}

			// 支持二进制多路复用：DATA:<id>|<payload>
			if len(msg) > 5 && string(msg[:5]) == "DATA:" {
				s := string(msg)
				parts := strings.SplitN(s[5:], "|", 2)
				if len(parts) == 2 {
					id := parts[0]
					payload := parts[1]
					p.mu.RLock()
					c := p.tcpMap[id]
					p.mu.RUnlock()
					if c != nil {
						if _, err := c.Write([]byte(payload)); err != nil {
							log.Printf("[客户端] 写入本地TCP连接失败: %v，发送CLOSE", err)
							go p.SendClose(id)
							c.Close()
							p.mu.Lock()
							delete(p.tcpMap, id)
							p.mu.Unlock()
						}
					} else {
						go p.SendClose(id)
					}
					continue
				}
			}
			p.mu.RLock()
			connID := p.boundByChannel[channelID]
			c := p.tcpMap[connID]
			p.mu.RUnlock()
			if connID != "" && c != nil {
				if _, err := c.Write(msg); err != nil {
					log.Printf("[客户端] 通道 %d 写入本地TCP连接失败: %v，发送CLOSE", channelID, err)
					go p.SendClose(connID)
					c.Close()
					p.mu.Lock()
					delete(p.tcpMap, connID)
					p.mu.Unlock()
				}
			}
			continue
		}

		if mt == websocket.TextMessage {
			data := string(msg)

			// UDP_CONNECTED
			if strings.HasPrefix(data, "UDP_CONNECTED:") {
				connID := strings.TrimPrefix(data, "UDP_CONNECTED:")
				p.mu.RLock()
				ch := p.connected[connID]
				p.mu.RUnlock()
				if ch != nil {
					select {
					case ch <- true:
					default:
					}
				}
				continue
			}

			// UDP_ERROR
			if strings.HasPrefix(data, "UDP_ERROR:") {
				parts := strings.SplitN(data[10:], "|", 2)
				if len(parts) == 2 {
					connID := parts[0]
					errMsg := parts[1]
					log.Printf("[客户端UDP:%s] 错误: %s", connID, errMsg)
				}
				continue
			}

			if strings.HasPrefix(data, "CLAIM_ACK:") {
				parts := strings.SplitN(data[10:], "|", 2)
				if len(parts) == 2 {
					connID := parts[0]
					p.mu.Lock()
					if _, exists := p.channelMap[connID]; exists {
						p.mu.Unlock()
						continue
					}
					info, ok := p.connInfo[connID]
					if !ok {
						p.mu.Unlock()
						continue
					}
					var latency float64
					if chTimes, ok := p.claimTimes[connID]; ok {
						if t, ok := chTimes[channelID]; ok {
							latency = float64(time.Since(t).Nanoseconds()) / 1e6
							delete(chTimes, channelID)
							if len(chTimes) == 0 {
								delete(p.claimTimes, connID)
							}
						}
					}
					p.channelMap[connID] = channelID
					p.boundByChannel[channelID] = connID
					delete(p.connInfo, connID)
					p.mu.Unlock()
					log.Printf("[客户端] 通道 %d 获胜，连接 %s，延迟 %.2fms", channelID, connID, latency)
					p.wsMutexes[channelID].Lock()
					err := wsConn.WriteMessage(websocket.TextMessage, []byte("TCP:"+connID+"|"+info.targetAddr+"|"+info.firstFrameData))
					p.wsMutexes[channelID].Unlock()
					if err != nil {
						p.mu.Lock()
						if c, ok := p.tcpMap[connID]; ok {
							c.Close()
							delete(p.tcpMap, connID)
						}
						delete(p.channelMap, connID)
						delete(p.boundByChannel, channelID)
						delete(p.connInfo, connID)
						delete(p.claimTimes, connID)
						p.mu.Unlock()
						continue
					}
				}
			} else if strings.HasPrefix(data, "CONNECTED:") {
				connID := strings.TrimPrefix(data, "CONNECTED:")
				p.mu.RLock()
				ch := p.connected[connID]
				p.mu.RUnlock()
				if ch != nil {
					select {
					case ch <- true:
					default:
					}
				}
			} else if strings.HasPrefix(data, "ERROR:") {
				log.Printf("[客户端] 通道 %d 错误: %s", channelID, data)
			} else if strings.HasPrefix(data, "CLOSE:") {
				id := strings.TrimPrefix(data, "CLOSE:")
				p.mu.Lock()
				if c, ok := p.tcpMap[id]; ok {
					_ = c.Close()
					delete(p.tcpMap, id)
				}
				delete(p.channelMap, id)
				delete(p.connInfo, id)
				delete(p.claimTimes, id)
				delete(p.boundByChannel, channelID)
				p.mu.Unlock()
			}
		}
	}
}

// redialChannel 重连指定通道
func (p *ECHPool) redialChannel(channelID int) {
	for {
		newConn, err := dialWebSocketWithECH(p.wsServerAddr, 2)
		if err != nil {
			time.Sleep(2 * time.Second)
			continue
		}
		p.wsConns[channelID] = newConn
		log.Printf("[客户端] 通道 %d 已重连", channelID)
		go p.handleChannel(channelID, newConn)
		return
	}
}

// SendData 发送TCP数据
func (p *ECHPool) SendData(connID string, b []byte) error {
	p.mu.RLock()
	chID, ok := p.channelMap[connID]
	var ws *websocket.Conn
	if ok && chID < len(p.wsConns) {
		ws = p.wsConns[chID]
	}
	p.mu.RUnlock()
	if !ok || ws == nil {
		return fmt.Errorf("未分配通道")
	}
	p.wsMutexes[chID].Lock()
	err := ws.WriteMessage(websocket.TextMessage, []byte("DATA:"+connID+"|"+string(b)))
	p.wsMutexes[chID].Unlock()
	return err
}

// SendClose 发送关闭连接消息
func (p *ECHPool) SendClose(connID string) error {
	p.mu.RLock()
	chID, ok := p.channelMap[connID]
	var ws *websocket.Conn
	if ok && chID < len(p.wsConns) {
		ws = p.wsConns[chID]
	}
	p.mu.RUnlock()
	if !ok || ws == nil {
		return nil
	}
	p.wsMutexes[chID].Lock()
	err := ws.WriteMessage(websocket.TextMessage, []byte("CLOSE:"+connID))
	p.wsMutexes[chID].Unlock()
	return err
}
