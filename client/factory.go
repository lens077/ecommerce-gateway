package client

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	config "github.com/go-kratos/gateway/api/gateway/config/v1"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/go-kratos/kratos/v2/selector"
	"github.com/go-kratos/kratos/v2/selector/p2c"
)

// Factory is returns service client.
type Factory func(*config.Endpoint) (Client, error)

type Option func(*options)
type options struct {
	pickerBuilder        selector.Builder
	enableHealthCheck    bool
	healthCheckInterval  time.Duration
	healthCheckTimeout   time.Duration
	maxHealthCheckRetries int
}

func WithPickerBuilder(in selector.Builder) Option {
	return func(o *options) {
		o.pickerBuilder = in
	}
}

// WithHealthCheck 启用健康检查
func WithHealthCheck(enable bool) Option {
	return func(o *options) {
		o.enableHealthCheck = enable
	}
}

// WithHealthCheckInterval 设置健康检查间隔
func WithHealthCheckInterval(interval time.Duration) Option {
	return func(o *options) {
		o.healthCheckInterval = interval
	}
}

// WithHealthCheckTimeout 设置健康检查超时时间
func WithHealthCheckTimeout(timeout time.Duration) Option {
	return func(o *options) {
		o.healthCheckTimeout = timeout
	}
}

// NewFactory new a client factory.
func NewFactory(r registry.Discovery, opts ...Option) Factory {
	o := &options{
		pickerBuilder:        p2c.NewBuilder(),
		enableHealthCheck:    true, // 默认启用健康检查
		healthCheckInterval:  10 * time.Second,
		healthCheckTimeout:   2 * time.Second,
		maxHealthCheckRetries: 3,
	}
	for _, opt := range opts {
		opt(o)
	}

	return func(endpoint *config.Endpoint) (Client, error) {
		picker := o.pickerBuilder.Build()
		ctx, cancel := context.WithCancel(context.Background())

		// 创建健康检查器（如果启用）
		var healthChecker HealthChecker
		if o.enableHealthCheck {
			healthChecker = NewHealthChecker(
				nil, // 初始节点为空，稍后通过 Callback 更新
				WithCheckInterval(o.healthCheckInterval),
				WithCheckTimeout(o.healthCheckTimeout),
				WithMaxFailures(o.maxHealthCheckRetries),
			)
			healthChecker.Start()
			log.Infof("Health checker enabled for endpoint: %s", endpoint.Path)
		}

		applier := &nodeApplier{
			cancel:        cancel,
			endpoint:      endpoint,
			registry:      r,
			picker:        picker,
			healthChecker: healthChecker,
		}
		if err := applier.apply(ctx); err != nil {
			if healthChecker != nil {
				healthChecker.Stop()
			}
			return nil, err
		}

		// 创建客户端（带健康检查）
		clientOpts := []ClientOption{}
		if healthChecker != nil {
			clientOpts = append(clientOpts, WithHealthChecker(healthChecker))
		}
		client := newClient(applier, picker, clientOpts...)

		// 如果是gRPC请求且路径以*结尾，创建grpcClient包装器
		// 例如：path: /search*，请求路径 /search/v1.SearchService/Search -> /v1.SearchService/Search
		// 这样JWT中间件就能看到完整的原始路径，正确匹配跳过规则
		// 而实际发送到后端的请求则是去除前缀后的路径
		if endpoint.Protocol == config.Protocol_GRPC && strings.HasSuffix(endpoint.Path, "*") {
			stripPrefix := strings.TrimSuffix(endpoint.Path, "*")
			return &grpcClient{
				client:      client,
				stripPrefix: stripPrefix,
				protocol:    endpoint.Protocol,
			}, nil
		}

		return client, nil
	}
}

// grpcClient 是对client的包装，用于处理gRPC请求的路径
// 当请求是gRPC且路径以*结尾时，自动去除前缀
// 例如：path: /search*，请求路径 /search/v1.SearchService/Search -> /v1.SearchService/Search
// 这样JWT中间件就能看到完整的原始路径，正确匹配跳过规则
// 而实际发送到后端的请求则是去除前缀后的路径

type grpcClient struct {
	client      *client
	stripPrefix string
	protocol    config.Protocol
}

// RoundTrip 实现 http.RoundTripper 接口
func (c *grpcClient) RoundTrip(req *http.Request) (*http.Response, error) {
	// 如果是gRPC请求且需要去除前缀
	if c.protocol == config.Protocol_GRPC && c.stripPrefix != "" {
		// 克隆请求，避免修改原始请求（原始请求用于中间件匹配）
		reqClone := req.Clone(req.Context())
		// 去除前缀
		if strings.HasPrefix(reqClone.URL.Path, c.stripPrefix) {
			newPath := strings.TrimPrefix(reqClone.URL.Path, c.stripPrefix)
			if newPath == "" {
				newPath = "/"
			} else if newPath[0] != '/' {
				newPath = "/" + newPath
			}
			reqClone.URL.Path = newPath
			// 使用克隆的请求发送到后端
			return c.client.RoundTrip(reqClone)
		}
	}
	// 其他情况直接使用原始请求
	return c.client.RoundTrip(req)
}

// Close 实现 io.Closer 接口
func (c *grpcClient) Close() error {
	return c.client.Close()
}

type nodeApplier struct {
	canceled        int64
	cancel          context.CancelFunc
	endpoint        *config.Endpoint
	registry        registry.Discovery
	picker          selector.Selector
	healthChecker   HealthChecker
	ctx             context.Context
	refreshTicker   *time.Ticker
}

func (na *nodeApplier) apply(ctx context.Context) error {
	// 保存 ctx
	na.ctx = ctx
	
	var nodes []selector.Node
	for _, backend := range na.endpoint.Backends {
		target, err := parseTarget(backend.Target)
		if err != nil {
			return err
		}
		switch target.Scheme {
		case "direct":
			weighted := backend.Weight // weight is only valid for direct scheme
			// 对于 direct 方案，使用解析后的 Authority 作为地址，而不是完整的 Target
			nodeAddr := target.Authority
			if nodeAddr == "" {
				nodeAddr = target.Endpoint
			}
			node := newNode(nodeAddr, na.endpoint.Protocol, weighted, map[string]string{}, "", "")
			nodes = append(nodes, node)
			na.picker.Apply(nodes)
		case "discovery":
			// 添加监听，该端点在注册中心中的实例列表都会写入到 na 中，且如果监听到服务列表的变化，则会调用na的回调
			existed := AddWatch(ctx, na.registry, target.Endpoint, na)
			if existed {
				log.Infof("watch target %+v already existed", target)
			}
			
			// 启动定期刷新机制，确保即使 watcher 不工作，也能获取最新的服务列表
			na.startRefreshLoop(target.Endpoint)
		default:
			return fmt.Errorf("unknown scheme: %s", target.Scheme)
		}
	}
	return nil
}

// startRefreshLoop 启动定期刷新服务列表的循环
func (na *nodeApplier) startRefreshLoop(serviceName string) {
	na.refreshTicker = time.NewTicker(15 * time.Second)
	
	go func() {
		for {
			select {
			case <-na.ctx.Done():
				na.refreshTicker.Stop()
				return
			case <-na.refreshTicker.C:
				// 主动从 Consul 获取最新的服务列表
				services, err := na.registry.GetService(na.ctx, serviceName)
				if err != nil {
					log.Warnf("Failed to refresh service list for %s: %v", serviceName, err)
					continue
				}
				if len(services) == 0 {
					log.Warnf("Empty service list for %s during refresh", serviceName)
					continue
				}
				
				log.Infof("Refreshed service list for %s, got %d instances", serviceName, len(services))
				// 更新服务列表
				na.Callback(services)
			}
		}
	}()
}

var _defaultWeight = int64(10)

func nodeWeight(n *registry.ServiceInstance) *int64 {
	w, ok := n.Metadata["weight"]
	if ok {
		val, _ := strconv.ParseInt(w, 10, 64)
		if val <= 0 {
			return &_defaultWeight
		}
		return &val
	}
	return &_defaultWeight
}

// Callback 节点应用的回调，会将注册中心的服务实例切片写入到选择器中 na.picker.Apply(nodes)
func (na *nodeApplier) Callback(services []*registry.ServiceInstance) error {
	if atomic.LoadInt64(&na.canceled) == 1 {
		return ErrCancelWatch
	}
	if len(services) == 0 {
		return nil
	}
	scheme := strings.ToLower(na.endpoint.Protocol.String())
	nodes := make([]selector.Node, 0, len(services))
	for _, ser := range services {
		addr, err := parseEndpoint(ser.Endpoints, scheme, false)
		if err != nil || addr == "" {
			// 如果没有找到匹配的grpc地址，尝试从http地址中提取端口信息构建grpc地址
			if scheme == "grpc" {
				port := extractPortFromEndpoints(ser.Endpoints)
				if port > 0 {
					// 使用本地地址或从http地址中提取的IP和端口
					addr = extractAddressFromEndpoints(ser.Endpoints, port)
					log.Infof("Using extracted endpoint address: %s for service: %s (scheme: %s)", addr, ser.Name, scheme)
				} else {
					log.Errorf("failed to parse endpoint: %v/%s: %v, and no port available", ser.Endpoints, scheme, err)
					continue
				}
			} else {
				log.Errorf("failed to parse endpoint: %v/%s: %v", ser.Endpoints, scheme, err)
				continue
			}
		}
		node := newNode(addr, na.endpoint.Protocol, nodeWeight(ser), ser.Metadata, ser.Version, ser.Name)
		nodes = append(nodes, node)
	}

	// 更新选择器的节点列表
	na.picker.Apply(nodes)

	// 更新健康检查器的节点列表（如果启用了健康检查）
	if na.healthChecker != nil {
		na.healthChecker.updateNodes(nodes)
		log.Infof("Updated health checker nodes for endpoint: %s, count: %d", na.endpoint.Path, len(nodes))
	}

	return nil
}

// extractPortFromEndpoints 从Endpoints中提取端口信息
func extractPortFromEndpoints(endpoints []string) int {
	for _, e := range endpoints {
		u, err := url.Parse(e)
		if err != nil {
			continue
		}
		_, portStr, err := net.SplitHostPort(u.Host)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		return port
	}
	return 0
}

// extractAddressFromEndpoints 从Endpoints中提取地址信息
func extractAddressFromEndpoints(endpoints []string, port int) string {
	for _, e := range endpoints {
		u, err := url.Parse(e)
		if err != nil {
			continue
		}
		// 尝试从HTTP地址中提取IP
		if u.Scheme == "http" {
			host, _, err := net.SplitHostPort(u.Host)
			if err != nil {
				// 如果没有端口，使用完整的host
				host = u.Host
			}
			// 构建新的地址，使用提取的IP和端口
			return fmt.Sprintf("%s:%d", host, port)
		}
	}
	// 如果没有找到HTTP地址，使用localhost
	return fmt.Sprintf("localhost:%d", port)
}

func (na *nodeApplier) Cancel() {
	log.Infof("Closing node applier for endpoint: %+v", na.endpoint)
	atomic.StoreInt64(&na.canceled, 1)
	na.cancel()
}

func (na *nodeApplier) Canceled() bool {
	return atomic.LoadInt64(&na.canceled) == 1
}
