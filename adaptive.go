package main

import (
	"sync"
	"time"
)

// ======================== 拥塞控制 (Aggressive Congestion Control) ========================

// ViolentCongestionController 实现了一个激进的拥塞控制算法
// 相比传统 TCP，它采用更大的初始窗口和更激进的增长策略
type ViolentCongestionController struct {
	mu sync.Mutex

	// 窗口控制参数
	cwnd     int // 当前拥塞窗口 (Congestion Window)，单位：字节
	ssthresh int // 慢启动阈值 (Slow Start Threshold)
	inFlight int // 已发送但未确认的数据量

	// 激进参数配置
	minWindow     int     // 最小窗口，保证基础速度
	maxWindow     int     // 最大窗口，防止过度发送
	growthRate    int     // 线性增长步长，单位：字节/ACK
	backoffFactor float64 // 丢包回退因子 (0.0-1.0)，越大越激进

	// 网络状态
	lastAckTime time.Time     // 最后一次收到 ACK 的时间
	rtt         time.Duration // 平滑的往返时延
	rttVar      time.Duration // RTT 变化量

	// 阻塞控制
	cond *sync.Cond
}

// NewViolentCongestionController 创建新的拥塞控制器
func NewViolentCongestionController() *ViolentCongestionController {
	const (
		initialWindow = 1 * 1024 * 1024  // 初始窗口 1MB
		minWindow     = 256 * 1024       // 最小窗口 256KB
		maxWindow     = 64 * 1024 * 1024 // 最大窗口 64MB
		ssthresh      = 10 * 1024 * 1024 // 慢启动阈值 10MB
		growthRate    = 128 * 1024       // 每次 ACK 增长 128KB (更加激进)
		backoffFactor = 0.95             // 丢包时降至 95% (几乎不退让)
	)

	c := &ViolentCongestionController{
		cwnd:          initialWindow,
		ssthresh:      ssthresh,
		minWindow:     minWindow,
		maxWindow:     maxWindow,
		growthRate:    growthRate,
		backoffFactor: backoffFactor,
		lastAckTime:   time.Now(),
	}
	c.cond = sync.NewCond(&c.mu)
	return c
}

// WaitWindow 阻塞直到有足够的窗口发送数据
func (c *ViolentCongestionController) WaitWindow(bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for {
		// 允许 1.5 倍窗口的超发 (极度激进)
		limit := int(float64(c.cwnd) * 1.5)
		if c.inFlight+bytes <= limit {
			return
		}
		c.cond.Wait()
	}
}

// OnDataSent 记录数据已发送，更新 inflight 计数
func (c *ViolentCongestionController) OnDataSent(bytes int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inFlight += bytes
}

// OnAck 处理收到的 ACK，更新窗口和 RTT
func (c *ViolentCongestionController) OnAck(bytes int, rtt time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 更新 inflight 计数
	c.inFlight -= bytes
	if c.inFlight < 0 {
		c.inFlight = 0
	}

	// 更新 RTT 估计值 (使用加权移动平均, EWMA)
	if c.rtt == 0 {
		c.rtt = rtt
		c.rttVar = rtt / 2
	} else {
		diff := c.rtt - rtt
		if diff < 0 {
			diff = -diff
		}
		c.rttVar = time.Duration(float64(c.rttVar)*0.75 + float64(diff)*0.25)
		c.rtt = time.Duration(float64(c.rtt)*0.875 + float64(rtt)*0.125)
	}

	// 激进增长：只要有 ACK 就增长
	c.cwnd += c.growthRate

	// 限制最大窗口
	if c.cwnd > c.maxWindow {
		c.cwnd = c.maxWindow
	}

	c.lastAckTime = time.Now()

	// 唤醒等待的发送者
	c.cond.Signal()
}

// OnLoss 处理丢包或超时事件
func (c *ViolentCongestionController) OnLoss() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// 激进回退
	c.cwnd = int(float64(c.cwnd) * c.backoffFactor)

	if c.cwnd < c.minWindow {
		c.cwnd = c.minWindow
	}
	c.ssthresh = c.cwnd

	// 即使回退也尝试唤醒，防止死锁
	c.cond.Signal()
}

// GetStats 获取状态
func (c *ViolentCongestionController) GetStats() (cwnd, inFlight int, rtt time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cwnd, c.inFlight, c.rtt
}

// ======================== 网络质量监控与自适应缓冲 ========================

// AdaptiveMonitor 监控网络传输速度并提供缓冲区大小建议
type AdaptiveMonitor struct {
	mu sync.RWMutex

	// 统计数据
	totalBytes int64     // 总传输字节数
	startTime  time.Time // 监控开始时间

	// 缓冲区配置
	bufferSize int // 当前缓冲区大小（固定 1MB）

	// 速度采样
	sampleBytes  int64     // 采样周期内的字节数
	sampleStart  time.Time // 当前采样周期开始时间
	currentSpeed float64   // 当前速度，单位：MB/s
}

// NewAdaptiveMonitor 创建新的自适应监控器
func NewAdaptiveMonitor() *AdaptiveMonitor {
	const bufferSize = 1 * 1024 * 1024 // 固定使用 1MB 缓冲区，最大化吞吐量

	return &AdaptiveMonitor{
		startTime:   time.Now(),
		bufferSize:  bufferSize,
		sampleStart: time.Now(),
	}
}

// Update 更新传输统计并计算速度
// 每隔 1 秒重新计算一次平均速度
func (m *AdaptiveMonitor) Update(bytes int) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// 累加总字节数和采样周期字节数
	m.totalBytes += int64(bytes)
	m.sampleBytes += int64(bytes)

	now := time.Now()
	duration := now.Sub(m.sampleStart)

	// 每秒更新一次速度
	if duration > 1*time.Second {
		seconds := duration.Seconds()
		if seconds > 0 {
			// 计算速度：MB/s
			m.currentSpeed = float64(m.sampleBytes) / (1024 * 1024) / seconds
		}
		// 重置采样周期
		m.sampleStart = now
		m.sampleBytes = 0
	}
}

// GetBufferSize 返回推荐的缓冲区大小
// 当前策略：固定返回 1MB，最大化吞吐量
func (m *AdaptiveMonitor) GetBufferSize() int {
	const fixedBufferSize = 1 * 1024 * 1024 // 1MB
	return fixedBufferSize
}

// GetSpeed 返回当前传输速度 (MB/s)
func (m *AdaptiveMonitor) GetSpeed() float64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentSpeed
}
