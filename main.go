package main

import (
	"flag"
	"log"
	"strings"
)

// 全局参数
var (
	listenAddr    string
	forwardAddr   string
	ipAddr        string
	certFile      string
	keyFile       string
	token         string
	cidrs         string
	connectionNum int

	// ECH/DNS 参数
	dnsServer string // -dns
	echDomain string // -ech

	// 多通道连接池
	echPool *ECHPool
)

func init() {
	flag.StringVar(&listenAddr, "l", "", "监听地址 (tcp://监听1/目标1,监听2/目标2,... 或 ws://ip:port/path 或 wss://ip:port/path 或 proxy://[user:pass@]ip:port)")
	flag.StringVar(&forwardAddr, "f", "", "服务地址 (格式: wss://host:port/path)")
	flag.StringVar(&ipAddr, "ip", "", "指定解析的IP地址（仅客户端：将 wss 主机名定向到该 IP 连接）")
	flag.StringVar(&certFile, "cert", "", "TLS证书文件路径（默认:自动生成，仅服务端）")
	flag.StringVar(&keyFile, "key", "", "TLS密钥文件路径（默认:自动生成，仅服务端）")
	flag.StringVar(&token, "token", "", "身份验证令牌（WebSocket Subprotocol）")
	flag.StringVar(&cidrs, "cidr", "0.0.0.0/0,::/0", "允许的来源 IP 范围 (CIDR),多个范围用逗号分隔")
	flag.StringVar(&dnsServer, "dns", "dns.alidns.com/dns-query", "查询 ECH 公钥所用的 DoH 服务器地址")
	flag.StringVar(&echDomain, "ech", "cloudflare-ech.com", "用于查询 ECH 公钥的域名")
	flag.IntVar(&connectionNum, "n", 3, "WebSocket连接数量")
}

func main() {
	flag.Parse()

	if strings.HasPrefix(listenAddr, "ws://") || strings.HasPrefix(listenAddr, "wss://") {
		runWebSocketServer(listenAddr)
		return
	}
	if strings.HasPrefix(listenAddr, "tcp://") {
		// 客户端模式：预先获取 ECH 公钥（失败则直接退出，严格禁止回退）
		if err := prepareECH(); err != nil {
			log.Fatalf("[客户端] 获取 ECH 公钥失败: %v", err)
		}
		runTCPClient(listenAddr, forwardAddr)
		return
	}
	if strings.HasPrefix(listenAddr, "proxy://") {
		// 代理模式（支持 SOCKS5 和 HTTP）：预先获取 ECH 公钥
		if err := prepareECH(); err != nil {
			log.Fatalf("[代理] 获取 ECH 公钥失败: %v", err)
		}
		runProxyServer(listenAddr, forwardAddr)
		return
	}

	log.Fatal("监听地址格式错误，请使用 ws://, wss://, tcp:// 或 proxy:// 前缀")
}
