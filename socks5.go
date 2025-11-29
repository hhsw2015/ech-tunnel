package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// SOCKS5 认证方法常量
const (
	NoAuth       = uint8(0x00)
	UserPassAuth = uint8(0x02)
	NoAcceptable = uint8(0xFF)
)

// SOCKS5 请求命令
const (
	ConnectCmd      = uint8(0x01)
	BindCmd         = uint8(0x02)
	UDPAssociateCmd = uint8(0x03)
)

// SOCKS5 地址类型
const (
	IPv4Addr   = uint8(0x01)
	DomainAddr = uint8(0x03)
	IPv6Addr   = uint8(0x04)
)

// SOCKS5 响应状态码
const (
	Succeeded               = uint8(0x00)
	GeneralFailure          = uint8(0x01)
	ConnectionNotAllowed    = uint8(0x02)
	NetworkUnreachable      = uint8(0x03)
	HostUnreachable         = uint8(0x04)
	ConnectionRefused       = uint8(0x05)
	TTLExpired              = uint8(0x06)
	CommandNotSupported     = uint8(0x07)
	AddressTypeNotSupported = uint8(0x08)
)

// UDPAssociation UDP关联结构（使用连接池）
type UDPAssociation struct {
	connID        string
	tcpConn       net.Conn
	udpListener   *net.UDPConn
	clientUDPAddr *net.UDPAddr
	pool          *ECHPool
	mu            sync.Mutex
	closed        bool
	done          chan bool
	connected     chan bool
	receiving     bool
}

// handleSOCKS5Protocol 处理 SOCKS5 协议
func handleSOCKS5Protocol(conn net.Conn, config *ProxyConfig, clientAddr string) {
	// 处理认证方法协商（需要读取剩余的认证方法）
	buf := make([]byte, 1)
	if _, err := io.ReadFull(conn, buf); err != nil {
		log.Printf("[SOCKS5:%s] 读取认证方法数量失败: %v", clientAddr, err)
		return
	}
	nMethods := buf[0]

	methods := make([]byte, nMethods)
	if _, err := io.ReadFull(conn, methods); err != nil {
		log.Printf("[SOCKS5:%s] 读取认证方法失败: %v", clientAddr, err)
		return
	}

	// 选择认证方法
	var method uint8 = NoAuth
	if config.Username != "" && config.Password != "" {
		method = UserPassAuth
		found := false
		for _, m := range methods {
			if m == UserPassAuth {
				found = true
				break
			}
		}
		if !found {
			method = NoAcceptable
		}
	}

	// 发送选择的认证方法
	response := []byte{0x05, method}
	if _, err := conn.Write(response); err != nil {
		log.Printf("[SOCKS5:%s] 发送认证方法响应失败: %v", clientAddr, err)
		return
	}

	if method == NoAcceptable {
		log.Printf("[SOCKS5:%s] 没有可接受的认证方法", clientAddr)
		return
	}

	// 处理用户名密码认证
	if method == UserPassAuth {
		if err := handleSOCKS5UserPassAuth(conn, config); err != nil {
			log.Printf("[SOCKS5:%s] 用户名密码认证失败: %v", clientAddr, err)
			return
		}
	}

	// 处理客户端请求
	if err := handleSOCKS5Request(conn, clientAddr, config); err != nil {
		log.Printf("[SOCKS5:%s] 处理请求失败: %v", clientAddr, err)
		return
	}
}

// handleSOCKS5UserPassAuth 处理 SOCKS5 用户名密码认证
func handleSOCKS5UserPassAuth(conn net.Conn, config *ProxyConfig) error {
	buf := make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("读取用户名密码认证头失败: %v", err)
	}

	version := buf[0]
	userLen := buf[1]

	if version != 1 {
		return fmt.Errorf("不支持的认证版本: %d", version)
	}

	// 读取用户名
	userBuf := make([]byte, userLen)
	if _, err := io.ReadFull(conn, userBuf); err != nil {
		return fmt.Errorf("读取用户名失败: %v", err)
	}

	// 读取密码长度
	passLenBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, passLenBuf); err != nil {
		return fmt.Errorf("读取密码长度失败: %v", err)
	}
	passLen := passLenBuf[0]

	// 读取密码
	passBuf := make([]byte, passLen)
	if _, err := io.ReadFull(conn, passBuf); err != nil {
		return fmt.Errorf("读取密码失败: %v", err)
	}

	// 验证用户名密码
	user := string(userBuf)
	pass := string(passBuf)

	var status byte = 0x00 // 0x00表示成功
	if user != config.Username || pass != config.Password {
		status = 0x01 // 认证失败
	}

	// 发送认证结果
	response := []byte{0x01, status}
	if _, err := conn.Write(response); err != nil {
		return fmt.Errorf("发送认证响应失败: %v", err)
	}

	if status != 0x00 {
		return fmt.Errorf("用户名或密码错误")
	}

	return nil
}

// handleSOCKS5Request 处理 SOCKS5 请求
func handleSOCKS5Request(conn net.Conn, clientAddr string, config *ProxyConfig) error {
	// 读取请求头
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("读取请求头失败: %v", err)
	}

	version := buf[0]
	command := buf[1]
	atyp := buf[3]

	if version != 5 {
		return fmt.Errorf("不支持的SOCKS版本: %d", version)
	}

	// 读取目标地址
	var host string
	switch atyp {
	case IPv4Addr:
		buf = make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return fmt.Errorf("读取IPv4地址失败: %v", err)
		}
		host = net.IP(buf).String()

	case DomainAddr:
		buf = make([]byte, 1)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return fmt.Errorf("读取域名长度失败: %v", err)
		}
		domainLen := buf[0]
		buf = make([]byte, domainLen)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return fmt.Errorf("读取域名失败: %v", err)
		}
		host = string(buf)

	case IPv6Addr:
		buf = make([]byte, 16)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return fmt.Errorf("读取IPv6地址失败: %v", err)
		}
		host = net.IP(buf).String()

	default:
		sendSOCKS5ErrorResponse(conn, AddressTypeNotSupported)
		return fmt.Errorf("不支持的地址类型: %d", atyp)
	}

	// 读取端口
	buf = make([]byte, 2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return fmt.Errorf("读取端口失败: %v", err)
	}
	port := int(buf[0])<<8 | int(buf[1])

	// 目标地址
	var target string
	if atyp == IPv6Addr {
		target = fmt.Sprintf("[%s]:%d", host, port)
	} else {
		target = fmt.Sprintf("%s:%d", host, port)
	}

	log.Printf("[SOCKS5:%s] 请求访问目标: %s (命令: %d)", clientAddr, target, command)

	// 处理不同的命令
	switch command {
	case ConnectCmd:
		return handleSOCKS5Connect(conn, target, clientAddr)
	case UDPAssociateCmd:
		return handleSOCKS5UDPAssociate(conn, clientAddr, config)
	case BindCmd:
		sendSOCKS5ErrorResponse(conn, CommandNotSupported)
		return fmt.Errorf("BIND命令暂不支持")
	default:
		sendSOCKS5ErrorResponse(conn, CommandNotSupported)
		return fmt.Errorf("不支持的命令类型: %d", command)
	}
}

// sendSOCKS5ErrorResponse 发送 SOCKS5 错误响应
func sendSOCKS5ErrorResponse(conn net.Conn, status uint8) {
	response := []byte{0x05, status, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	conn.Write(response)
}

// sendSOCKS5SuccessResponse 发送 SOCKS5 成功响应
func sendSOCKS5SuccessResponse(conn net.Conn) error {
	// 简单返回成功响应（绑定地址为 0.0.0.0:0）
	response := []byte{0x05, Succeeded, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
	_, err := conn.Write(response)
	return err
}

// handleSOCKS5Connect 处理 SOCKS5 CONNECT 命令
func handleSOCKS5Connect(conn net.Conn, target, clientAddr string) error {
	connID := uuid.New().String()
	_ = conn.SetDeadline(time.Time{})
	_ = conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	buffer := make([]byte, 32768)
	n, _ := conn.Read(buffer)
	_ = conn.SetReadDeadline(time.Time{})
	first := ""
	if n > 0 {
		first = string(buffer[:n])
	}

	echPool.RegisterAndClaim(connID, target, first, conn)
	if !echPool.WaitConnected(connID, 5*time.Second) {
		sendSOCKS5ErrorResponse(conn, GeneralFailure)
		return fmt.Errorf("SOCKS5 CONNECT 超时")
	}
	if err := sendSOCKS5SuccessResponse(conn); err != nil {
		return fmt.Errorf("发送SOCKS5成功响应失败: %v", err)
	}

	defer func() {
		_ = echPool.SendClose(connID)
		_ = conn.Close()
		echPool.mu.Lock()
		delete(echPool.tcpMap, connID)
		echPool.mu.Unlock()
		log.Printf("[SOCKS5:%s] 连接断开，已发送 CLOSE 通知", clientAddr)
	}()

	buf := make([]byte, 32768)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return nil
		}
		if err := echPool.SendData(connID, buf[:n]); err != nil {
			log.Printf("[SOCKS5] 发送数据到通道失败: %v", err)
			return err
		}
	}
}

// handleSOCKS5UDPAssociate 处理UDP ASSOCIATE请求（使用ECH连接池）
func handleSOCKS5UDPAssociate(tcpConn net.Conn, clientAddr string, config *ProxyConfig) error {
	log.Printf("[SOCKS5:%s] 处理UDP ASSOCIATE请求（使用连接池）", clientAddr)

	// 获取SOCKS5服务器的监听IP（根据配置）
	host, _, err := net.SplitHostPort(config.Host)
	if err != nil {
		sendSOCKS5ErrorResponse(tcpConn, GeneralFailure)
		return fmt.Errorf("解析监听地址失败: %v", err)
	}

	// 创建UDP监听器（端口由系统自动分配，IP使用配置的监听IP）
	udpAddr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, "0"))
	if err != nil {
		sendSOCKS5ErrorResponse(tcpConn, GeneralFailure)
		return fmt.Errorf("解析UDP地址失败: %v", err)
	}

	udpListener, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		sendSOCKS5ErrorResponse(tcpConn, GeneralFailure)
		return fmt.Errorf("UDP监听失败: %v", err)
	}
	defer udpListener.Close()

	// 获取实际监听的端口
	actualAddr := udpListener.LocalAddr().(*net.UDPAddr)
	log.Printf("[SOCKS5:%s] UDP中继服务器启动: %s（通过连接池）", clientAddr, actualAddr.String())

	// 发送成功响应（包含UDP中继服务器的地址和端口）
	err = sendSOCKS5UDPResponse(tcpConn, actualAddr)
	if err != nil {
		return fmt.Errorf("发送UDP响应失败: %v", err)
	}

	// 生成连接ID并创建UDP关联
	connID := uuid.New().String()
	assoc := &UDPAssociation{
		connID:      connID,
		tcpConn:     tcpConn,
		udpListener: udpListener,
		pool:        echPool,
		done:        make(chan bool, 2),
		connected:   make(chan bool, 1),
	}

	// 注册到连接池
	echPool.RegisterUDP(connID, assoc)

	log.Printf("[SOCKS5:%s] UDP关联已创建，连接ID: %s", clientAddr, connID)

	// 清除TCP连接超时（保持连接活跃）
	tcpConn.SetDeadline(time.Time{})

	// 启动UDP数据处理goroutine
	go assoc.handleUDPRelay()

	// 监听TCP控制连接（阻塞等待）
	go func() {
		buf := make([]byte, 1)
		for {
			_, err := tcpConn.Read(buf)
			if err != nil {
				log.Printf("[SOCKS5:%s] TCP控制连接断开，终止UDP关联", clientAddr)
				assoc.done <- true
				return
			}
		}
	}()

	// 等待结束信号（TCP断开或UDP出错）
	<-assoc.done

	assoc.Close()
	log.Printf("[SOCKS5:%s] UDP关联已终止，连接ID: %s", clientAddr, connID)

	return nil
}

// sendSOCKS5UDPResponse 发送UDP ASSOCIATE成功响应
func sendSOCKS5UDPResponse(conn net.Conn, udpAddr *net.UDPAddr) error {
	response := make([]byte, 0, 22)
	response = append(response, 0x05, Succeeded, 0x00)

	// 地址类型和地址
	ip := udpAddr.IP
	if ip4 := ip.To4(); ip4 != nil {
		// IPv4
		response = append(response, IPv4Addr)
		response = append(response, ip4...)
	} else {
		// IPv6
		response = append(response, IPv6Addr)
		response = append(response, ip...)
	}

	// 端口
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, uint16(udpAddr.Port))
	response = append(response, port...)

	_, err := conn.Write(response)
	return err
}

// handleUDPRelay 处理UDP数据中继（使用连接池）
func (assoc *UDPAssociation) handleUDPRelay() {
	buffer := make([]byte, 65535)

	for {
		n, srcAddr, err := assoc.udpListener.ReadFromUDP(buffer)
		if err != nil {
			if !isNormalCloseError(err) {
				log.Printf("[UDP:%s] 读取失败: %v", assoc.connID, err)
			}
			assoc.done <- true
			return
		}

		// 第一次收到UDP包时，记录客户端UDP地址
		if assoc.clientUDPAddr == nil {
			assoc.mu.Lock()
			if assoc.clientUDPAddr == nil {
				assoc.clientUDPAddr = srcAddr
				log.Printf("[UDP:%s] 客户端UDP地址: %s", assoc.connID, srcAddr.String())
			}
			assoc.mu.Unlock()
		} else {
			// 验证UDP包来自正确的客户端
			if assoc.clientUDPAddr.String() != srcAddr.String() {
				log.Printf("[UDP:%s] 忽略来自未授权地址的UDP包: %s", assoc.connID, srcAddr.String())
				continue
			}
		}

		log.Printf("[UDP:%s] 收到UDP数据包，大小: %d", assoc.connID, n)

		// 处理UDP数据包
		go assoc.handleUDPPacket(buffer[:n])
	}
}

// handleUDPPacket 处理单个UDP数据包（通过连接池）
func (assoc *UDPAssociation) handleUDPPacket(packet []byte) {
	// 解析SOCKS5 UDP请求头
	target, data, err := parseSOCKS5UDPPacket(packet)
	if err != nil {
		log.Printf("[UDP:%s] 解析UDP数据包失败: %v", assoc.connID, err)
		return
	}

	log.Printf("[UDP:%s] 目标: %s, 数据长度: %d", assoc.connID, target, len(data))

	// 通过连接池发送数据
	if err := assoc.sendUDPData(target, data); err != nil {
		log.Printf("[UDP:%s] 发送数据失败: %v", assoc.connID, err)
		return
	}
}

// sendUDPData 通过连接池发送UDP数据
func (assoc *UDPAssociation) sendUDPData(target string, data []byte) error {
	assoc.mu.Lock()
	defer assoc.mu.Unlock()

	if assoc.closed {
		return fmt.Errorf("关联已关闭")
	}

	// 只在第一次发送时建立连接
	if !assoc.receiving {
		assoc.receiving = true
		// 发送UDP_CONNECT消息（包含目标地址）
		if err := assoc.pool.SendUDPConnect(assoc.connID, target); err != nil {
			return fmt.Errorf("发送UDP_CONNECT失败: %v", err)
		}

		// 等待连接成功
		go func() {
			if !assoc.pool.WaitConnected(assoc.connID, 5*time.Second) {
				log.Printf("[UDP:%s] 连接超时", assoc.connID)
				assoc.done <- true
				return
			}
			log.Printf("[UDP:%s] 连接已建立", assoc.connID)
		}()
	}

	// 发送实际数据
	if err := assoc.pool.SendUDPData(assoc.connID, data); err != nil {
		return fmt.Errorf("发送UDP数据失败: %v", err)
	}

	return nil
}

// handleUDPResponse 处理从WebSocket返回的UDP数据
func (assoc *UDPAssociation) handleUDPResponse(addrData string, data []byte) {
	// 解析地址 "host:port"
	parts := strings.Split(addrData, ":")
	if len(parts) != 2 {
		log.Printf("[UDP:%s] 无效的地址格式: %s", assoc.connID, addrData)
		return
	}

	host := parts[0]
	port := 0
	fmt.Sscanf(parts[1], "%d", &port)

	// 构建SOCKS5 UDP响应包
	packet, err := buildSOCKS5UDPPacket(host, port, data)
	if err != nil {
		log.Printf("[UDP:%s] 构建响应包失败: %v", assoc.connID, err)
		return
	}

	// 发送回客户端
	if assoc.clientUDPAddr != nil {
		assoc.mu.Lock()
		_, err = assoc.udpListener.WriteToUDP(packet, assoc.clientUDPAddr)
		assoc.mu.Unlock()

		if err != nil {
			log.Printf("[UDP:%s] 发送UDP响应失败: %v", assoc.connID, err)
			assoc.done <- true
			return
		}

		log.Printf("[UDP:%s] 已发送UDP响应: %s:%d, 大小: %d", assoc.connID, host, port, len(data))
	}
}

// IsClosed 检查关联是否已关闭
func (assoc *UDPAssociation) IsClosed() bool {
	assoc.mu.Lock()
	defer assoc.mu.Unlock()
	return assoc.closed
}

// Close 关闭UDP关联
func (assoc *UDPAssociation) Close() {
	assoc.mu.Lock()
	defer assoc.mu.Unlock()

	if assoc.closed {
		return
	}

	assoc.closed = true

	// 通过连接池关闭UDP连接
	if assoc.pool != nil {
		assoc.pool.SendUDPClose(assoc.connID)
	}

	if assoc.udpListener != nil {
		assoc.udpListener.Close()
	}

	log.Printf("[UDP:%s] 关联资源已清理", assoc.connID)
}

// parseSOCKS5UDPPacket 解析SOCKS5 UDP数据包
func parseSOCKS5UDPPacket(packet []byte) (string, []byte, error) {
	if len(packet) < 10 {
		return "", nil, fmt.Errorf("数据包太短")
	}

	// RSV (2字节) + FRAG (1字节)
	if packet[0] != 0 || packet[1] != 0 {
		return "", nil, fmt.Errorf("无效的RSV字段")
	}

	frag := packet[2]
	if frag != 0 {
		return "", nil, fmt.Errorf("不支持分片 (FRAG=%d)", frag)
	}

	atyp := packet[3]
	offset := 4

	var host string
	switch atyp {
	case IPv4Addr:
		if len(packet) < offset+4 {
			return "", nil, fmt.Errorf("IPv4地址不完整")
		}
		host = net.IP(packet[offset : offset+4]).String()
		offset += 4

	case DomainAddr:
		if len(packet) < offset+1 {
			return "", nil, fmt.Errorf("域名长度字段缺失")
		}
		domainLen := int(packet[offset])
		offset++
		if len(packet) < offset+domainLen {
			return "", nil, fmt.Errorf("域名数据不完整")
		}
		host = string(packet[offset : offset+domainLen])
		offset += domainLen

	case IPv6Addr:
		if len(packet) < offset+16 {
			return "", nil, fmt.Errorf("IPv6地址不完整")
		}
		host = net.IP(packet[offset : offset+16]).String()
		offset += 16

	default:
		return "", nil, fmt.Errorf("不支持的地址类型: %d", atyp)
	}

	// 端口
	if len(packet) < offset+2 {
		return "", nil, fmt.Errorf("端口字段缺失")
	}
	port := int(packet[offset])<<8 | int(packet[offset+1])
	offset += 2

	// 实际数据
	data := packet[offset:]

	var target string
	if atyp == IPv6Addr {
		target = fmt.Sprintf("[%s]:%d", host, port)
	} else {
		target = fmt.Sprintf("%s:%d", host, port)
	}

	return target, data, nil
}

// buildSOCKS5UDPPacket 构建SOCKS5 UDP响应数据包
func buildSOCKS5UDPPacket(host string, port int, data []byte) ([]byte, error) {
	packet := make([]byte, 0, 1024)

	// RSV (2字节) + FRAG (1字节)
	packet = append(packet, 0x00, 0x00, 0x00)

	// 解析地址类型
	ip := net.ParseIP(host)
	if ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			// IPv4
			packet = append(packet, IPv4Addr)
			packet = append(packet, ip4...)
		} else {
			// IPv6
			packet = append(packet, IPv6Addr)
			packet = append(packet, ip...)
		}
	} else {
		// 域名
		if len(host) > 255 {
			return nil, fmt.Errorf("域名过长")
		}
		packet = append(packet, DomainAddr)
		packet = append(packet, byte(len(host)))
		packet = append(packet, []byte(host)...)
	}

	// 端口
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	packet = append(packet, portBytes...)

	// 数据
	packet = append(packet, data...)

	return packet, nil
}
