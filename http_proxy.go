package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

// handleHTTPProtocol 处理 HTTP 代理协议
func handleHTTPProtocol(conn net.Conn, config *ProxyConfig, clientAddr string, firstByte byte) {
	// 读取完整的第一行（HTTP 请求行）
	reader := bufio.NewReader(io.MultiReader(bytes.NewReader([]byte{firstByte}), conn))

	// 读取请求行
	requestLine, err := reader.ReadString('\n')
	if err != nil {
		log.Printf("[HTTP:%s] 读取请求行失败: %v", clientAddr, err)
		return
	}

	// 解析请求行: METHOD URL HTTP/VERSION
	parts := strings.SplitN(strings.TrimSpace(requestLine), " ", 3)
	if len(parts) != 3 {
		log.Printf("[HTTP:%s] 无效的请求行: %s", clientAddr, requestLine)
		return
	}

	method := parts[0]
	requestURL := parts[1]

	log.Printf("[HTTP:%s] %s %s", clientAddr, method, requestURL)

	// CONNECT 方法：建立隧道
	if method == "CONNECT" {
		handleHTTPConnect(conn, reader, config, clientAddr, requestURL)
		return
	}

	// 其他方法（GET, POST 等）：转发 HTTP 请求
	handleHTTPForward(conn, reader, config, clientAddr, method, requestURL)
}

// handleHTTPConnect 处理 HTTP CONNECT 方法（用于 HTTPS）
func handleHTTPConnect(conn net.Conn, reader *bufio.Reader, config *ProxyConfig, clientAddr, target string) {
	log.Printf("[HTTP:%s] CONNECT 到 %s", clientAddr, target)

	// 读取并验证请求头（包括认证）
	headers, err := readHTTPHeaders(reader)
	if err != nil {
		log.Printf("[HTTP:%s] 读取请求头失败: %v", clientAddr, err)
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}

	// 验证认证（如果配置了）
	if config.Username != "" && config.Password != "" {
		authHeader := headers["Proxy-Authorization"]
		if !validateProxyAuth(authHeader, config.Username, config.Password) {
			log.Printf("[HTTP:%s] 认证失败", clientAddr)
			conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"Proxy\"\r\n\r\n"))
			return
		}
	}

	// 使用连接池建立连接
	connID := uuid.New().String()
	_ = conn.SetDeadline(time.Time{})

	echPool.RegisterAndClaim(connID, target, "", conn)
	if !echPool.WaitConnected(connID, 5*time.Second) {
		log.Printf("[HTTP:%s] CONNECT 超时", clientAddr)
		conn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\n\r\n"))
		return
	}

	// 发送成功响应
	_, err = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	if err != nil {
		log.Printf("[HTTP:%s] 发送响应失败: %v", clientAddr, err)
		return
	}

	log.Printf("[HTTP:%s] CONNECT 隧道已建立到 %s", clientAddr, target)

	defer func() {
		_ = echPool.SendClose(connID)
		_ = conn.Close()
		echPool.mu.Lock()
		delete(echPool.tcpMap, connID)
		echPool.mu.Unlock()
		log.Printf("[HTTP:%s] CONNECT 隧道关闭", clientAddr)
	}()

	// 转发数据
	buf := make([]byte, 32768)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		if err := echPool.SendData(connID, buf[:n]); err != nil {
			log.Printf("[HTTP:%s] 发送数据失败: %v", clientAddr, err)
			return
		}
	}
}

// handleHTTPForward 处理普通 HTTP 请求（GET, POST 等）
func handleHTTPForward(conn net.Conn, reader *bufio.Reader, config *ProxyConfig, clientAddr, method, requestURL string) {
	log.Printf("[HTTP:%s] 转发 %s %s", clientAddr, method, requestURL)

	// 解析目标 URL
	parsedURL, err := url.Parse(requestURL)
	if err != nil {
		log.Printf("[HTTP:%s] 解析 URL 失败: %v", clientAddr, err)
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}

	// 读取请求头
	headers, err := readHTTPHeaders(reader)
	if err != nil {
		log.Printf("[HTTP:%s] 读取请求头失败: %v", clientAddr, err)
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}

	// 验证认证（如果配置了）
	if config.Username != "" && config.Password != "" {
		authHeader := headers["Proxy-Authorization"]
		if !validateProxyAuth(authHeader, config.Username, config.Password) {
			log.Printf("[HTTP:%s] 认证失败", clientAddr)
			conn.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"Proxy\"\r\n\r\n"))
			return
		}
	}

	// 确定目标地址
	target := parsedURL.Host
	if !strings.Contains(target, ":") {
		if parsedURL.Scheme == "https" {
			target += ":443"
		} else {
			target += ":80"
		}
	}

	// 读取请求体（如果有）
	var bodyData []byte
	if contentLength, ok := headers["Content-Length"]; ok {
		var length int
		fmt.Sscanf(contentLength, "%d", &length)
		if length > 0 && length < 10*1024*1024 { // 限制最大 10MB
			bodyData = make([]byte, length)
			_, err := io.ReadFull(reader, bodyData)
			if err != nil {
				log.Printf("[HTTP:%s] 读取请求体失败: %v", clientAddr, err)
				conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
				return
			}
		}
	}

	// 构建转发请求
	var requestBuffer bytes.Buffer

	// 修改请求行：使用相对路径
	path := parsedURL.Path
	if path == "" {
		path = "/"
	}
	if parsedURL.RawQuery != "" {
		path += "?" + parsedURL.RawQuery
	}
	requestBuffer.WriteString(fmt.Sprintf("%s %s HTTP/1.1\r\n", method, path))

	// 写入请求头（移除代理相关头部）
	for key, value := range headers {
		if key != "Proxy-Authorization" && key != "Proxy-Connection" {
			requestBuffer.WriteString(fmt.Sprintf("%s: %s\r\n", key, value))
		}
	}

	// 确保有 Host 头
	if _, ok := headers["Host"]; !ok {
		requestBuffer.WriteString(fmt.Sprintf("Host: %s\r\n", parsedURL.Host))
	}

	requestBuffer.WriteString("\r\n")

	// 写入请求体
	if len(bodyData) > 0 {
		requestBuffer.Write(bodyData)
	}

	firstFrameData := requestBuffer.String()

	// 使用连接池建立连接
	connID := uuid.New().String()
	_ = conn.SetDeadline(time.Time{})

	echPool.RegisterAndClaim(connID, target, firstFrameData, conn)
	if !echPool.WaitConnected(connID, 5*time.Second) {
		log.Printf("[HTTP:%s] 连接超时", clientAddr)
		conn.Write([]byte("HTTP/1.1 504 Gateway Timeout\r\n\r\n"))
		return
	}

	log.Printf("[HTTP:%s] 请求已转发到 %s", clientAddr, target)

	defer func() {
		_ = echPool.SendClose(connID)
		_ = conn.Close()
		echPool.mu.Lock()
		delete(echPool.tcpMap, connID)
		echPool.mu.Unlock()
		log.Printf("[HTTP:%s] 请求处理完成", clientAddr)
	}()

	// 等待响应（响应会通过连接池返回到 conn）
	// 这里只需要保持连接，直到任一方关闭
	buf := make([]byte, 32768)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return
		}
		// 客户端发送的后续数据（如果有）也转发
		if err := echPool.SendData(connID, buf[:n]); err != nil {
			log.Printf("[HTTP:%s] 发送数据失败: %v", clientAddr, err)
			return
		}
	}
}

// readHTTPHeaders 读取 HTTP 请求头
func readHTTPHeaders(reader *bufio.Reader) (map[string]string, error) {
	headers := make(map[string]string)

	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}

		line = strings.TrimSpace(line)
		if line == "" {
			break // 空行表示头部结束
		}

		// 解析头部：Key: Value
		parts := strings.SplitN(line, ":", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			value := strings.TrimSpace(parts[1])
			headers[key] = value
		}
	}

	return headers, nil
}

// validateProxyAuth 验证 HTTP 代理认证
func validateProxyAuth(authHeader, username, password string) bool {
	if authHeader == "" {
		return false
	}

	// 解析 Basic 认证：Basic <base64>
	const prefix = "Basic "
	if !strings.HasPrefix(authHeader, prefix) {
		return false
	}

	encoded := strings.TrimPrefix(authHeader, prefix)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}

	// 格式：username:password
	credentials := string(decoded)
	parts := strings.SplitN(credentials, ":", 2)
	if len(parts) != 2 {
		return false
	}

	return parts[0] == username && parts[1] == password
}
