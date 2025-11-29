# ECH Tunnel - 加密客户端Hello隧道程序

## 项目简介

ECH Tunnel 是一个基于 Go 语言开发的加密网络隧道工具，支持多种运行模式，主要用于在受限网络环境中建立安全的加密隧道。该程序利用了 **ECH (Encrypted Client Hello)** 技术，这是 TLS 1.3 的一个扩展，能够加密 TLS 握手的客户端 Hello 消息，防止网络审查和流量分析。

## 项目结构

```
server2/
├── main.go              # 主程序入口，命令行参数解析和模式选择
├── utils.go             # 工具函数（网络错误判断）
├── ech.go               # ECH 相关功能（DNS查询、ECH配置获取）
├── websocket_server.go  # WebSocket 服务端实现
├── tcp_client.go        # TCP 客户端实现（正向转发）
├── pool.go              # 多通道连接池管理
├── proxy.go             # 代理服务器入口
├── socks5.go            # SOCKS5 代理协议实现
├── http_proxy.go        # HTTP/HTTPS 代理协议实现
├── go.mod               # Go 模块依赖配置
└── go.sum               # Go 模块依赖校验
```

## 核心功能详解

### 1. ECH (Encrypted Client Hello) 技术

**功能模块**: `ech.go`

**原理解析**:

ECH 是 TLS 1.3 的一个扩展协议，旨在解决传统 TLS 握手过程中的隐私泄露问题。在传统 TLS 中，客户端 Hello 消息（包含 SNI - Server Name Indication）是明文传输的，这意味着中间人可以看到客户端想要访问的具体域名。

**ECH 的工作流程**:

1. **DNS 查询阶段**: 客户端通过 DoH (DNS over HTTPS) 查询目标域名的 HTTPS 记录（DNS 类型 65），从中获取 ECH 公钥配置列表（ECHConfigList）。

2. **配置解析**: 程序解析 DNS 响应中的 SvcParams，提取 key=5 的参数（即 echconfig），这是一个 Base64 编码的 ECH 公钥。

3. **TLS 握手**: 使用获取的 ECH 公钥，客户端将原本明文的 Client Hello 内容（包括 SNI）加密后放入 TLS 握手消息中。

4. **防回退机制**: 程序实现了严格的 ECH 验证，如果服务器拒绝 ECH，连接会直接失败，不会回退到明文 SNI，确保了安全性。

**技术细节**:
- 使用阿里云 DoH 服务器 (`dns.alidns.com/dns-query`) 进行 DNS 查询
- 默认查询 Cloudflare 的 ECH 配置域名 (`cloudflare-ech.com`)
- 支持 ECH 配置自动刷新和重试机制
- 完全基于 TLS 1.3，不支持更低版本

### 2. WebSocket 隧道服务端

**功能模块**: `websocket_server.go`

**原理解析**:

WebSocket 服务端是整个隧道系统的核心枢纽，负责接收客户端的连接并将流量转发到实际的目标服务器。

**多路复用机制**:

程序实现了高效的多路复用协议，允许单个 WebSocket 连接同时处理多个 TCP/UDP 会话：

1. **连接标识**: 每个会话使用 UUID 作为唯一标识符（connID）
2. **协议格式**: 
   - `TCP:<connID>|<target>|<firstFrame>` - 建立 TCP 连接
   - `DATA:<connID>|<payload>` - 传输数据
   - `CLOSE:<connID>` - 关闭连接
   - `UDP_CONNECT:<connID>|<target>` - 建立 UDP 关联
   - `UDP_DATA:<connID>|<data>` - 传输 UDP 数据

3. **并发处理**: 使用 Goroutine 为每个会话创建独立的处理协程，通过 Context 机制统一管理生命周期

**安全特性**:

- **IP 白名单**: 支持 CIDR 格式的 IP 访问控制
- **Token 认证**: 通过 WebSocket Subprotocol 实现简单的身份验证
- **TLS 加密**: 支持 wss:// 协议，可使用自签名证书或提供的证书
- **保活机制**: 实现了 Ping/Pong 心跳检测

### 3. TCP 客户端（正向转发）

**功能模块**: `tcp_client.go`

**原理解析**:

TCP 客户端模式用于将本地 TCP 端口转发到远程服务器，适用于需要穿透防火墙或 NAT 的场景。

**多通道竞速机制**:

这是程序的一大创新点，通过建立多条并行的 WebSocket 连接来提高可靠性和性能：

1. **连接池**: 启动时创建 N 条（默认 3 条）WebSocket 长连接
2. **竞选算法**: 当有新的 TCP 连接时，向所有通道发送 CLAIM 消息
3. **延迟选择**: 第一个响应 CLAIM_ACK 的通道获胜，用于后续数据传输
4. **动态绑定**: 每个 TCP 会话绑定到延迟最低的通道，实现负载均衡

**首帧优化**:

程序实现了"首帧捕获"技术，在 TCP 连接建立后立即读取第一个数据包，并与连接请求一起发送，减少往返次数（RTT），这对 HTTP/HTTPS 等协议特别有效。

### 4. 多通道连接池

**功能模块**: `pool.go`

**原理解析**:

ECHPool 是一个复杂的连接池管理器，负责维护多条 WebSocket 连接的状态和消息路由。

**核心数据结构**:

```go
type ECHPool struct {
    wsConns      []*websocket.Conn    // WebSocket 连接数组
    wsMutexes    []sync.Mutex         // 每个连接的写锁
    tcpMap       map[string]net.Conn  // connID -> TCP连接映射
    udpMap       map[string]*UDPAssoc // connID -> UDP关联映射
    channelMap   map[string]int       // connID -> 通道ID映射
    connInfo     map[string]struct{}  // 待绑定连接信息
    claimTimes   map[string]map[int]time.Time // 竞选延迟记录
    connected    map[string]chan bool // 连接成功信号通道
}
```

**工作流程**:

1. **注册**: `RegisterAndClaim()` 注册新连接并向所有通道发起竞选
2. **绑定**: 接收 `CLAIM_ACK` 后将连接绑定到响应最快的通道
3. **路由**: 根据 connID 查找对应通道，确保消息发送到正确的 WebSocket
4. **重连**: 当某个通道断开时，自动重连并恢复服务

**并发控制**:

使用细粒度的锁机制，为每个 WebSocket 连接分配独立的互斥锁，避免了全局锁的性能瓶颈。

### 5. SOCKS5 代理

**功能模块**: `socks5.go`

**原理解析**:

SOCKS5 是一个通用的代理协议，支持 TCP 和 UDP 流量转发。

**协议实现**:

1. **认证协商**: 
   - 支持无认证（0x00）和用户名密码认证（0x02）
   - 服务端可配置强制认证

2. **请求处理**: 
   - CONNECT (0x01): 建立 TCP 隧道
   - UDP ASSOCIATE (0x03): 建立 UDP 中继

3. **地址类型**: 
   - IPv4 (0x01)
   - 域名 (0x03)
   - IPv6 (0x04)

**UDP 中继原理**:

UDP 中继是 SOCKS5 最复杂的功能之一：

1. **三方通信**: 客户端通过 TCP 控制连接发起 UDP ASSOCIATE，服务端返回 UDP 中继地址
2. **数据封装**: UDP 数据包使用 SOCKS5 协议封装（包含目标地址信息）
3. **地址验证**: 服务端验证 UDP 包来源，防止未授权访问
4. **生命周期**: UDP 关联绑定到 TCP 控制连接，TCP 断开时 UDP 也会关闭

### 6. HTTP/HTTPS 代理

**功能模块**: `http_proxy.go`

**原理解析**:

HTTP 代理支持两种模式：普通 HTTP 请求转发和 HTTPS CONNECT 隧道。

**CONNECT 隧道**:

```
客户端 -> 发送 CONNECT example.com:443 HTTP/1.1
代理   -> 建立到目标的连接
代理   -> 返回 HTTP/1.1 200 Connection Established
此后   -> 透明传输 TLS 加密流量
```

**普通 HTTP 转发**:

1. **请求重写**: 将绝对 URI 转换为相对路径
2. **头部过滤**: 移除 Proxy-Authorization 等代理专用头部
3. **首帧发送**: 将完整的 HTTP 请求作为首帧数据发送，减少往返

**认证机制**:

支持 HTTP Basic 认证，用户名密码 Base64 编码后通过 Proxy-Authorization 头部传输。

## 运行模式

### 1. WebSocket 服务端模式

```bash
# 使用自签名证书
./ech-tunnel -l wss://0.0.0.0:8443/tunnel -token mytoken -cidr 10.0.0.0/8

# 使用自定义证书
./ech-tunnel -l wss://0.0.0.0:8443/tunnel -cert server.crt -key server.key
```

### 2. TCP 正向转发模式

```bash
# 转发本地 8080 到远程 80
./ech-tunnel -l tcp://127.0.0.1:8080/example.com:80 -f wss://server.com:8443/tunnel -token mytoken

# 多端口转发
./ech-tunnel -l tcp://127.0.0.1:8080/web:80,127.0.0.1:8443/web:443 -f wss://server.com:8443/tunnel
```

### 3. 代理模式

```bash
# 无认证代理
./ech-tunnel -l proxy://127.0.0.1:1080 -f wss://server.com:8443/tunnel

# 带认证代理
./ech-tunnel -l proxy://user:pass@127.0.0.1:1080 -f wss://server.com:8443/tunnel
```

## 技术优势

1. **高度隐蔽**: ECH 技术加密 SNI，防止域名泄露
2. **性能优异**: 多通道竞速机制，自动选择最优路径
3. **协议完备**: 支持 TCP、UDP、HTTP、HTTPS、SOCKS5 多种协议
4. **可靠性强**: 连接池自动重连，单点故障不影响服务
5. **易于部署**: 单个二进制文件，跨平台支持

## 应用场景

1. **穿透防火墙**: 绕过企业或学校的网络限制
2. **隐私保护**: 防止 ISP 或中间人监控访问的域名
3. **负载均衡**: 多通道分流，提高传输效率
4. **网络加速**: 通过优选路径减少延迟
5. **统一代理**: 单一入口支持多种客户端类型

## 依赖说明

- **github.com/google/uuid**: UUID 生成，用于连接标识
- **github.com/gorilla/websocket**: WebSocket 协议实现
- **crypto/tls**: Go 标准库 TLS 1.3 支持（含 ECH）

## 安全注意事项

1. **密钥管理**: 妥善保管 TLS 证书私钥
2. **Token 强度**: 使用足够长的随机 token
3. **访问控制**: 合理配置 CIDR 白名单
4. **版本更新**: 及时更新以修复安全漏洞
5. **审计日志**: 监控异常连接和流量模式

## 原理总结

这个程序的核心创新在于**将 ECH 加密技术与多通道 WebSocket 隧道相结合**，实现了既安全又高效的网络代理方案。通过 DNS over HTTPS 获取 ECH 配置，确保了配置获取过程的安全性；通过多路复用和竞速机制，实现了单连接承载多会话的高效传输；通过支持多种代理协议，满足了不同应用场景的需求。整个系统架构清晰，模块化程度高，易于维护和扩展。
