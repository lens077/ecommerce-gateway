package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-kratos/kratos/v2/selector"
)

var (
	// 默认健康检查配置
	defaultCheckInterval = 10 * time.Second
	defaultCheckTimeout  = 2 * time.Second
	defaultMaxFailures   = 3
)

// HealthChecker 健康检查器接口
type HealthChecker interface {
	Start()
	Stop()
	IsHealthy(node selector.Node) bool
	MarkUnhealthy(node selector.Node)
	HealthyNodeFilter() func(selector.Node) bool
	updateNodes(nodes []selector.Node)
}

// healthChecker 健康检查器实现
type healthChecker struct {
	mu           sync.RWMutex
	healthyNodes map[string]bool          // 节点地址 -> 健康状态
	failureCount map[string]int           // 节点地址 -> 失败计数
	nodes        map[string]selector.Node // 节点地址 -> 节点实例
	interval     time.Duration
	timeout      time.Duration
	maxFailures  int
	ticker       *time.Ticker
	ctx          context.Context
	cancel       context.CancelFunc
	httpClient   *http.Client
}

// NewHealthChecker 创建健康检查器
func NewHealthChecker(nodes []selector.Node, opts ...HealthCheckerOption) HealthChecker {
	ctx, cancel := context.WithCancel(context.Background())

	hc := &healthChecker{
		healthyNodes: make(map[string]bool),
		failureCount: make(map[string]int),
		nodes:        make(map[string]selector.Node),
		interval:     defaultCheckInterval,
		timeout:      defaultCheckTimeout,
		maxFailures:  defaultMaxFailures,
		ctx:          ctx,
		cancel:       cancel,
		httpClient: &http.Client{
			Timeout: defaultCheckTimeout,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   500 * time.Millisecond,
					KeepAlive: 30 * time.Second,
				}).DialContext,
			},
		},
	}

	// 应用选项
	for _, opt := range opts {
		opt(hc)
	}

	// 注册初始节点
	for _, node := range nodes {
		hc.registerNode(node)
	}

	return hc
}

// HealthCheckerOption 健康检查器选项
type HealthCheckerOption func(*healthChecker)

// WithCheckInterval 设置健康检查间隔
func WithCheckInterval(interval time.Duration) HealthCheckerOption {
	return func(hc *healthChecker) {
		hc.interval = interval
	}
}

// WithCheckTimeout 设置健康检查超时时间
func WithCheckTimeout(timeout time.Duration) HealthCheckerOption {
	return func(hc *healthChecker) {
		hc.timeout = timeout
		hc.httpClient.Timeout = timeout
	}
}

// WithMaxFailures 设置最大失败次数
func WithMaxFailures(maxFailures int) HealthCheckerOption {
	return func(hc *healthChecker) {
		hc.maxFailures = maxFailures
	}
}

// registerNode 注册节点
func (hc *healthChecker) registerNode(node selector.Node) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	addr := node.Address()
	hc.nodes[addr] = node
	hc.healthyNodes[addr] = true // 默认标记为健康
	hc.failureCount[addr] = 0
}

// unregisterNode 注销节点
func (hc *healthChecker) unregisterNode(node selector.Node) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	addr := node.Address()
	delete(hc.nodes, addr)
	delete(hc.healthyNodes, addr)
	delete(hc.failureCount, addr)
}

// updateNodes 更新节点列表
func (hc *healthChecker) updateNodes(nodes []selector.Node) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	// 先注销所有旧节点
	for addr := range hc.nodes {
		delete(hc.nodes, addr)
		delete(hc.healthyNodes, addr)
		delete(hc.failureCount, addr)
	}

	// 注册新节点
	for _, node := range nodes {
		addr := node.Address()
		hc.nodes[addr] = node
		hc.healthyNodes[addr] = true
		hc.failureCount[addr] = 0
	}
}

// Start 启动健康检查
func (hc *healthChecker) Start() {
	if hc.ticker != nil {
		return
	}

	hc.ticker = time.NewTicker(hc.interval)
	go hc.runCheckLoop()
	LOG.Info("Health checker started")
}

// Stop 停止健康检查
func (hc *healthChecker) Stop() {
	if hc.ticker != nil {
		hc.ticker.Stop()
		hc.ticker = nil
	}
	hc.cancel()
	LOG.Info("Health checker stopped")
}

// IsHealthy 检查节点是否健康
func (hc *healthChecker) IsHealthy(node selector.Node) bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	healthy, ok := hc.healthyNodes[node.Address()]
	return ok && healthy
}

// MarkUnhealthy 标记节点为不健康
func (hc *healthChecker) MarkUnhealthy(node selector.Node) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	addr := node.Address()
	if _, ok := hc.failureCount[addr]; ok {
		hc.failureCount[addr]++
		if hc.failureCount[addr] >= hc.maxFailures {
			hc.healthyNodes[addr] = false
			LOG.Warnf("Node marked as unhealthy: %s (failures: %d)", addr, hc.failureCount[addr])
		}
	}
}

// runCheckLoop 运行健康检查循环
func (hc *healthChecker) runCheckLoop() {
	for {
		select {
		case <-hc.ctx.Done():
			return
		case <-hc.ticker.C:
			hc.checkAllNodes()
		}
	}
}

// checkAllNodes 检查所有节点的健康状态
func (hc *healthChecker) checkAllNodes() {
	hc.mu.RLock()
	nodes := make([]selector.Node, 0, len(hc.nodes))
	for _, node := range hc.nodes {
		nodes = append(nodes, node)
	}
	hc.mu.RUnlock()

	var wg sync.WaitGroup
	for _, node := range nodes {
		wg.Add(1)
		go func(n selector.Node) {
			defer wg.Done()
			hc.checkNode(n)
		}(node)
	}
	wg.Wait()
}

// checkNode 检查单个节点的健康状态
func (hc *healthChecker) checkNode(node selector.Node) {
	addr := node.Address()
	protocol := node.Scheme()

	// 构建健康检查 URL
	var url string
	if protocol == "grpc" {
		// gRPC 服务使用 HTTP/2 健康检查
		url = fmt.Sprintf("http://%s/healthz", addr)
	} else {
		url = fmt.Sprintf("http://%s/healthz", addr)
	}

	req, err := http.NewRequestWithContext(hc.ctx, http.MethodGet, url, nil)
	if err != nil {
		LOG.Errorf("Failed to create health check request for %s: %v", addr, err)
		hc.markNodeFailure(addr)
		return
	}

	resp, err := hc.httpClient.Do(req)
	if err != nil {
		LOG.Warnf("Health check failed for %s: %v", addr, err)
		hc.markNodeFailure(addr)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		LOG.Warnf("Health check failed for %s: status code %d", addr, resp.StatusCode)
		hc.markNodeFailure(addr)
		return
	}

	// 健康检查通过，重置失败计数并标记为健康
	hc.mu.Lock()
	hc.failureCount[addr] = 0
	prevHealthy := hc.healthyNodes[addr]
	hc.healthyNodes[addr] = true
	hc.mu.Unlock()

	if !prevHealthy {
		LOG.Infof("Node recovered: %s", addr)
	}
}

// markNodeFailure 标记节点失败
func (hc *healthChecker) markNodeFailure(addr string) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	if _, ok := hc.failureCount[addr]; ok {
		hc.failureCount[addr]++
		if hc.failureCount[addr] >= hc.maxFailures {
			hc.healthyNodes[addr] = false
			LOG.Warnf("Node marked as unhealthy after %d failures: %s", hc.maxFailures, addr)
		}
	}
}

// HealthyNodeFilter 健康节点过滤器
func (hc *healthChecker) HealthyNodeFilter() func(selector.Node) bool {
	return func(node selector.Node) bool {
		return hc.IsHealthy(node)
	}
}

// 健康检查错误类型
var (
	ErrAllNodesUnhealthy = errors.New("all nodes are unhealthy")
	ErrNoAvailableNodes  = errors.New("no available nodes")
)
