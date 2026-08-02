package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/go-kratos/gateway/constants"
	"github.com/go-kratos/kratos/v2/selector"
)

var (
	defaultMaxFailures = 3
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
		interval:     constants.HealthCheckInterval,
		timeout:      constants.HealthCheckTimeout,
		maxFailures:  defaultMaxFailures,
		ctx:          ctx,
		cancel:       cancel,
		httpClient: &http.Client{
			Timeout: constants.HealthCheckTimeout,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   constants.HealthCheckDialTimeout,
					KeepAlive: constants.HealthCheckKeepAlive,
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

// updateNodes 用注册中心推来的最新实例列表对齐节点集合。
//
// 这里必须是增量语义(diff),不能全删再全加。全删再全加会把每个仍然存在的节点的
// healthyNodes/failureCount 一并重置成「健康、0 次失败」—— 服务发现每推送一次
// 就等于给所有节点做一次赦免,失败计数永远攒不到 maxFailures,健康检查器实际上
// 标不出任何不健康节点,HealthyNodeFilter 形同虚设。
//
// 所以:只有新出现的地址才初始化为健康,只有消失的地址才被删除,已存在的地址
// 保留它当前的健康状态和失败计数,只刷新 node 实例本身(权重、元数据可能变了)。
func (hc *healthChecker) updateNodes(nodes []selector.Node) {
	hc.mu.Lock()
	defer hc.mu.Unlock()

	latest := make(map[string]selector.Node, len(nodes))
	for _, node := range nodes {
		latest[node.Address()] = node
	}

	// 已消失的实例:连同它的健康状态一起清掉,避免地址复用时继承旧计数
	for addr := range hc.nodes {
		if _, ok := latest[addr]; !ok {
			delete(hc.nodes, addr)
			delete(hc.healthyNodes, addr)
			delete(hc.failureCount, addr)
		}
	}

	for addr, node := range latest {
		if _, existed := hc.nodes[addr]; !existed {
			// 新实例:先当健康的放行,后续由 checkNode 修正
			hc.healthyNodes[addr] = true
			hc.failureCount[addr] = 0
		}
		hc.nodes[addr] = node
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
