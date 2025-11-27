package main

import (
	"sync"
	"time"
)

// ======================== 暴力拥塞控制 (Violent Congestion Control) ========================

type ViolentCongestionController struct {
	mu sync.Mutex

	// 窗口控制
	cwnd          int     // 当前拥塞窗口 (Congestion Window)
	ssthresh      int     // 慢启动阈值
	inFlight      int     // 已发送但未确认的数据量
	
	// 激进参数
	minWindow     int     // 最小窗口 (保底速度)
	maxWindow     int     // 最大窗口
	growthRate    int     // 线性增长步长 (字节)
	backoffFactor float64 // 回退因子 (0.0 - 1.0, 越大越暴力)

	// 状态
	lastAckTime   time.Time
	rtt           time.Duration
	rttVar        time.Duration
}

func NewViolentCongestionController() *ViolentCongestionController {
	return &ViolentCongestionController{
		// 暴力配置: 初始窗口直接 1MB, 极大提升启动速度
		cwnd:          1024 * 1024, 
		ssthresh:      1024 * 1024 * 10, // 阈值设得很高
		
		// 限制
		minWindow:     256 * 1024,       // 最小不低于 256KB
		maxWindow:     1024 * 1024 * 64, // 最大允许 64MB 窗口
		
		// 增长与回退
		growthRate:    65536, // 每次ACK增加 64KB (非常激进)
		backoffFactor: 0.9,   // 丢包时只降到 90% (标准TCP是50%)
		
		lastAckTime:   time.Now(),
	}
}

// CanSend 检查是否允许发送
func (c *ViolentCongestionController) CanSend(bytes int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 暴力模式: 允许一定程度的超发 (1.2倍窗口)
	limit := int(float64(c.cwnd) * 1.2)
	return c.inFlight+bytes <= limit
}

// OnDataSent 记录数据发送
func (c *ViolentCongestionController) OnDataSent(bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlight += bytes
}

// OnAck 处理确认 (核心逻辑)
func (c *ViolentCongestionController) OnAck(bytes int, rtt time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.inFlight -= bytes
	if c.inFlight < 0 {
		c.inFlight = 0
	}

	// 更新 RTT (加权移动平均)
	if c.rtt == 0 {
		c.rtt = rtt
		c.rttVar = rtt / 2
	} else {
		diff := c.rtt - rtt
		if diff < 0 { diff = -diff }
		c.rttVar = time.Duration(float64(c.rttVar)*0.75 + float64(diff)*0.25)
		c.rtt = time.Duration(float64(c.rtt)*0.875 + float64(rtt)*0.125)
	}

	// 暴力增长策略: 只要有ACK就增长, 不管是慢启动还是拥塞避免
	// 线性增长 (Additive Increase), 但步长很大
	c.cwnd += c.growthRate

	// 限制最大窗口
	if c.cwnd > c.maxWindow {
		c.cwnd = c.maxWindow
	}

	c.lastAckTime = time.Now()
}

// OnLoss 处理丢包/超时
func (c *ViolentCongestionController) OnLoss() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 暴力回退: 仅轻微减少窗口
	c.cwnd = int(float64(c.cwnd) * c.backoffFactor)
	
	// 保证底线
	if c.cwnd < c.minWindow {
		c.cwnd = c.minWindow
	}
	
	// 调整阈值
	c.ssthresh = c.cwnd
}

// GetStats 获取状态
func (c *ViolentCongestionController) GetStats() (cwnd, inFlight int, rtt time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cwnd, c.inFlight, c.rtt
}

// ======================== 网络质量监控与自适应缓冲 ========================

type AdaptiveMonitor struct {
	mu sync.RWMutex
	
	totalBytes    int64
	startTime     time.Time
	
	// 动态调整的参数
	bufferSize    int
	
	// 采样
	sampleBytes   int64
	sampleStart   time.Time
	currentSpeed  float64 // MB/s
}

func NewAdaptiveMonitor() *AdaptiveMonitor {
	return &AdaptiveMonitor{
		startTime:   time.Now(),
		bufferSize:  256 * 1024, // 初始 256KB
		sampleStart: time.Now(),
	}
}

func (m *AdaptiveMonitor) Update(bytes int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.totalBytes += int64(bytes)
	m.sampleBytes += int64(bytes)
	
	now := time.Now()
	duration := now.Sub(m.sampleStart)
	
	// 每 500ms 更新一次采样
	if duration > 500*time.Millisecond {
		// 计算速度 (MB/s)
		seconds := duration.Seconds()
		if seconds > 0 {
			m.currentSpeed = float64(m.sampleBytes) / 1024 / 1024 / seconds
		}
		
		// 自适应调整缓冲区
		// 策略: 缓冲区大小 = 当前速度 * 100ms (保持高吞吐所需的最小缓冲)
		// 但为了暴力性能, 我们直接乘以 200ms - 500ms
		targetBuffer := int(m.currentSpeed * 1024 * 1024 * 0.5) 
		
		// 限制范围
		if targetBuffer < 64*1024 {
			targetBuffer = 64 * 1024
		}
		if targetBuffer > 4*1024*1024 { // 最大 4MB
			targetBuffer = 4 * 1024 * 1024
		}
		
		// 平滑调整
		m.bufferSize = (m.bufferSize + targetBuffer) / 2
		
		// 重置采样
		m.sampleStart = now
		m.sampleBytes = 0
	}
}

func (m *AdaptiveMonitor) GetBufferSize() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.bufferSize
}

func (m *AdaptiveMonitor) GetSpeed() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentSpeed
}
