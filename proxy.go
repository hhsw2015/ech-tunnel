package main

import (
	"fmt"
	"log"
	"net"
	"net/url"
	"strings"
	"time"
)

// ProxyConfig 代理服务器配置
type ProxyConfig struct {
	Username string
	Password string
	Host     string
}

// parseProxyAddr 解析代理地址
func parseProxyAddr(addr string) (*ProxyConfig, error) {
	// 格式: proxy://[user:pass@]ip:port
	addr = strings.TrimPrefix(addr, "proxy://")

	config := &ProxyConfig{}

	// 检查是否有认证信息
	if strings.Contains(addr, "@") {
		parts := strings.SplitN(addr, "@", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("无效的代理地址格式")
		}

		// 解析用户名密码
		auth := parts[0]
		if strings.Contains(auth, ":") {
			authParts := strings.SplitN(auth, ":", 2)
			config.Username = authParts[0]
			config.Password = authParts[1]
		}

		config.Host = parts[1]
	} else {
		config.Host = addr
	}

	return config, nil
}

// runProxyServer 运行代理服务器（支持 SOCKS5 和 HTTP）
func runProxyServer(addr, wsServerAddr string) {
	if wsServerAddr == "" {
		log.Fatal("代理服务器需要指定 WebSocket 服务端地址 (-f)")
	}

	// 验证必须使用 wss://（强制 ECH）
	u, err := url.Parse(wsServerAddr)
	if err != nil {
		log.Fatalf("解析 WebSocket 服务端地址失败: %v", err)
	}
	if u.Scheme != "wss" {
		log.Fatalf("[代理] 仅支持 wss://（客户端必须使用 ECH/TLS1.3）")
	}

	config, err := parseProxyAddr(addr)
	if err != nil {
		log.Fatalf("解析代理地址失败: %v", err)
	}

	listener, err := net.Listen("tcp", config.Host)
	if err != nil {
		log.Fatalf("代理监听失败 %s: %v", config.Host, err)
	}
	defer listener.Close()

	log.Printf("代理服务器启动（支持 SOCKS5 和 HTTP）监听: %s", config.Host)
	if config.Username != "" {
		log.Printf("代理认证已启用，用户名: %s", config.Username)
	}

	echPool = NewECHPool(wsServerAddr, connectionNum)
	echPool.Start()

	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Printf("接受连接失败: %v", err)
			continue
		}

		go handleProxyConnection(conn, config)
	}
}

// handleProxyConnection 处理代理连接（自动检测协议类型）
func handleProxyConnection(conn net.Conn, config *ProxyConfig) {
	defer conn.Close()

	clientAddr := conn.RemoteAddr().String()
	log.Printf("[代理:%s] 新连接", clientAddr)

	// 设置连接超时
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	// 读取第一个字节判断协议类型
	buf := make([]byte, 1)
	if _, err := conn.Read(buf); err != nil {
		log.Printf("[代理:%s] 读取第一个字节失败: %v", clientAddr, err)
		return
	}

	firstByte := buf[0]

	// SOCKS5: 第一个字节是 0x05
	if firstByte == 0x05 {
		log.Printf("[代理:%s] 检测到 SOCKS5 协议", clientAddr)
		handleSOCKS5Protocol(conn, config, clientAddr)
		return
	}

	// HTTP: 第一个字节是字母 (GET, POST, CONNECT, HEAD, PUT, DELETE, OPTIONS, PATCH)
	if firstByte == 'G' || firstByte == 'P' || firstByte == 'C' || firstByte == 'H' ||
		firstByte == 'D' || firstByte == 'O' {
		log.Printf("[代理:%s] 检测到 HTTP 协议", clientAddr)
		handleHTTPProtocol(conn, config, clientAddr, firstByte)
		return
	}

	log.Printf("[代理:%s] 未知协议，第一个字节: 0x%02X", clientAddr, firstByte)
}
