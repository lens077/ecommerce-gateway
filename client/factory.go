package client

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

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
	pickerBuilder selector.Builder
}

func WithPickerBuilder(in selector.Builder) Option {
	return func(o *options) {
		o.pickerBuilder = in
	}
}

// NewFactory new a client factory.
func NewFactory(r registry.Discovery, opts ...Option) Factory {
	o := &options{
		pickerBuilder: p2c.NewBuilder(),
	}
	for _, opt := range opts {
		opt(o)
	}
	return func(endpoint *config.Endpoint) (Client, error) {
		picker := o.pickerBuilder.Build()
		ctx, cancel := context.WithCancel(context.Background())
		applier := &nodeApplier{
			cancel:   cancel,
			endpoint: endpoint,
			registry: r,
			picker:   picker,
		}
		if err := applier.apply(ctx); err != nil {
			return nil, err
		}
		client := newClient(applier, picker)

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
	canceled int64
	cancel   context.CancelFunc
	endpoint *config.Endpoint
	registry registry.Discovery
	picker   selector.Selector
}

func (na *nodeApplier) apply(ctx context.Context) error {
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
		default:
			return fmt.Errorf("unknown scheme: %s", target.Scheme)
		}
	}
	return nil
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
			log.Errorf("failed to parse endpoint: %v/%s: %v", ser.Endpoints, scheme, err)
			continue
		}
		node := newNode(addr, na.endpoint.Protocol, nodeWeight(ser), ser.Metadata, ser.Version, ser.Name)
		nodes = append(nodes, node)
	}
	na.picker.Apply(nodes)
	return nil
}

func (na *nodeApplier) Cancel() {
	log.Infof("Closing node applier for endpoint: %+v", na.endpoint)
	atomic.StoreInt64(&na.canceled, 1)
	na.cancel()
}

func (na *nodeApplier) Canceled() bool {
	return atomic.LoadInt64(&na.canceled) == 1
}
