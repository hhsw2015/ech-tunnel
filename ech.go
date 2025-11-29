package main

import (
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// DNS查询相关常量
const (
	typeHTTPS = 65 // DNS HTTPS 记录类型
)

var (
	// 运行期缓存的 ECHConfigList
	echListMu sync.RWMutex
	echList   []byte
)

// prepareECH 客户端启动时查询 ECH 配置并缓存
func prepareECH() error {
	for {
		log.Printf("[客户端] 使用 DNS 服务器查询 ECH: %s -> %s", dnsServer, echDomain)
		echBase64, err := queryHTTPSRecord(echDomain, dnsServer)
		if err != nil {
			log.Printf("[客户端] DNS 查询失败: %v，2秒后重试...", err)
			time.Sleep(2 * time.Second)
			continue
		}
		if echBase64 == "" {
			log.Printf("[客户端] 未找到 ECH 参数（HTTPS RR key=echconfig/5），2秒后重试...")
			time.Sleep(2 * time.Second)
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(echBase64)
		if err != nil {
			log.Printf("[客户端] ECH Base64 解码失败: %v，2秒后重试...", err)
			time.Sleep(2 * time.Second)
			continue
		}
		echListMu.Lock()
		echList = raw
		echListMu.Unlock()
		log.Printf("[客户端] ECHConfigList 长度: %d 字节", len(raw))
		return nil
	}
}

// refreshECH 刷新 ECH 配置（用于重试）
func refreshECH() error {
	log.Printf("[ECH] 刷新 ECH 公钥配置...")
	return prepareECH()
}

// getECHList 获取当前的 ECH 配置列表
func getECHList() ([]byte, error) {
	echListMu.RLock()
	defer echListMu.RUnlock()
	if len(echList) == 0 {
		return nil, errors.New("ECH 配置尚未加载")
	}
	return echList, nil
}

// queryHTTPSRecord 查询 DNS HTTPS 记录
func queryHTTPSRecord(domain, dnsServer string) (string, error) {
	dohURL := dnsServer
	if !strings.HasPrefix(dohURL, "https://") && !strings.HasPrefix(dohURL, "http://") {
		dohURL = "https://" + dohURL
	}
	return queryDoH(domain, dohURL)
}

// queryDoH 通过 DoH (DNS over HTTPS) 查询
func queryDoH(domain, dohURL string) (string, error) {
	u, err := url.Parse(dohURL)
	if err != nil {
		return "", fmt.Errorf("无效的 DoH URL: %v", err)
	}
	q := u.Query()
	q.Set("name", domain)
	q.Set("type", "HTTPS")
	dnsQuery := buildDNSQuery(domain, typeHTTPS)
	dnsBase64 := base64.RawURLEncoding.EncodeToString(dnsQuery)

	q.Set("dns", dnsBase64)
	// 移除 name 和 type，因为使用了 dns 参数
	q.Del("name")
	q.Del("type")

	u.RawQuery = q.Encode()

	req, err := http.NewRequest("GET", u.String(), nil)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %v", err)
	}
	req.Header.Set("Accept", "application/dns-message")
	req.Header.Set("Content-Type", "application/dns-message")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("DoH 请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("DoH 服务器返回错误: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读取 DoH 响应失败: %v", err)
	}

	return parseDNSResponse(body)
}

// buildDNSQuery 构建 DNS 查询报文
func buildDNSQuery(domain string, qtype uint16) []byte {
	query := make([]byte, 0, 512)
	// Header
	query = append(query, 0x00, 0x01)                         // ID
	query = append(query, 0x01, 0x00)                         // 标准查询
	query = append(query, 0x00, 0x01)                         // QDCOUNT = 1
	query = append(query, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00) // AN/NS/AR = 0
	// QNAME
	for _, label := range strings.Split(domain, ".") {
		query = append(query, byte(len(label)))
		query = append(query, []byte(label)...)
	}
	query = append(query, 0x00) // root
	// QTYPE/QCLASS
	query = append(query, byte(qtype>>8), byte(qtype))
	query = append(query, 0x00, 0x01) // IN
	return query
}

// parseDNSResponse 解析 DNS 响应报文
func parseDNSResponse(response []byte) (string, error) {
	if len(response) < 12 {
		return "", fmt.Errorf("响应长度无效")
	}
	ancount := binary.BigEndian.Uint16(response[6:8])
	if ancount == 0 {
		return "", fmt.Errorf("未找到回答记录")
	}
	// 跳过 Question
	offset := 12
	for offset < len(response) && response[offset] != 0 {
		offset += int(response[offset]) + 1
	}
	offset += 5 // null + type + class

	// Answers
	for i := 0; i < int(ancount); i++ {
		if offset >= len(response) {
			break
		}
		// NAME（可能压缩）
		if response[offset]&0xC0 == 0xC0 {
			offset += 2
		} else {
			for offset < len(response) && response[offset] != 0 {
				offset += int(response[offset]) + 1
			}
			offset++
		}
		if offset+10 > len(response) {
			break
		}
		rrType := binary.BigEndian.Uint16(response[offset : offset+2])
		offset += 8 // type(2) + class(2) + ttl(4)
		dataLen := binary.BigEndian.Uint16(response[offset : offset+2])
		offset += 2
		if offset+int(dataLen) > len(response) {
			break
		}
		data := response[offset : offset+int(dataLen)]
		offset += int(dataLen)

		if rrType == typeHTTPS {
			if ech := parseHTTPSRecord(data); ech != "" {
				return ech, nil
			}
		}
	}
	return "", nil
}

// parseHTTPSRecord 解析 HTTPS 记录，仅抽取 SvcParamKey == 5 (ECHConfigList/echconfig)
func parseHTTPSRecord(data []byte) string {
	if len(data) < 2 {
		return ""
	}
	// 跳 priority(2)
	offset := 2
	// 跳 targetName
	if offset < len(data) && data[offset] == 0 {
		offset++
	} else {
		for offset < len(data) && data[offset] != 0 {
			offset += int(data[offset]) + 1
		}
		offset++
	}
	// SvcParams
	for offset+4 <= len(data) {
		key := binary.BigEndian.Uint16(data[offset : offset+2])
		length := binary.BigEndian.Uint16(data[offset+2 : offset+4])
		offset += 4
		if offset+int(length) > len(data) {
			break
		}
		value := data[offset : offset+int(length)]
		offset += int(length)
		if key == 5 {
			return base64.StdEncoding.EncodeToString(value)
		}
	}
	return ""
}
