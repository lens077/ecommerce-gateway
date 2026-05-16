package client

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/go-kratos/gateway/middleware"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/selector"
)

var (
	LOG = log.NewHelper(log.With(log.GetLogger(), "source", "client"))

	// 默认重试次数
	defaultMaxRetries = 3
)

type client struct {
	applier     *nodeApplier
	selector    selector.Selector
	healthCheck HealthChecker
	maxRetries  int
}

type Client interface {
	http.RoundTripper
	io.Closer
}

type ClientOption func(*client)

// WithMaxRetries 设置最大重试次数
func WithMaxRetries(maxRetries int) ClientOption {
	return func(c *client) {
		c.maxRetries = maxRetries
	}
}

// WithHealthChecker 设置健康检查器
func WithHealthChecker(hc HealthChecker) ClientOption {
	return func(c *client) {
		c.healthCheck = hc
	}
}

func newClient(applier *nodeApplier, selector selector.Selector, opts ...ClientOption) *client {
	c := &client{
		applier:    applier,
		selector:   selector,
		maxRetries: defaultMaxRetries,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

func (c *client) Close() error {
	c.applier.Cancel()
	if c.healthCheck != nil {
		c.healthCheck.Stop()
	}
	return nil
}

// RoundTrip implements http.RoundTripper. RoundTripper 先进后出栈的第一个元素
func (c *client) RoundTrip(req *http.Request) (resp *http.Response, err error) {
	ctx := req.Context()
	reqOpt, _ := middleware.FromRequestContext(ctx)

	var lastErr error
	var lastDone func(context.Context, selector.DoneInfo)

	for attempt := 0; attempt < c.maxRetries; attempt++ {
		// 构建节点过滤器（包含健康检查）
		filters := []selector.NodeFilter{}
		if c.healthCheck != nil {
			// 将健康检查过滤器转换为 selector.NodeFilter
			healthFilter := c.healthCheck.HealthyNodeFilter()
			filters = append(filters, func(ctx context.Context, nodes []selector.Node) []selector.Node {
				healthyNodes := make([]selector.Node, 0, len(nodes))
				for _, node := range nodes {
					if healthFilter(node) {
						healthyNodes = append(healthyNodes, node)
					}
				}
				return healthyNodes
			})
		}
		if ctxFilters, ok := middleware.SelectorFiltersFromContext(ctx); ok {
			filters = append(filters, ctxFilters...)
		}

		// 选择节点
		n, done, err := c.selector.Select(ctx, selector.WithNodeFilter(filters...))
		if err != nil {
			lastErr = err
			LOG.Warnf("Failed to select node (attempt %d/%d): %v", attempt+1, c.maxRetries, err)
			continue
		}
		lastDone = done
		reqOpt.CurrentNode = n

		addr := n.Address()
		reqOpt.Backends = append(reqOpt.Backends, addr)

		// 执行请求
		reqCopy := req.Clone(ctx)
		reqCopy.URL.Host = addr
		reqCopy.URL.Scheme = "http"
		reqCopy.RequestURI = ""

		startAt := time.Now()
		resp, err = n.(*node).client.Do(reqCopy)
		reqOpt.UpstreamResponseTime = append(reqOpt.UpstreamResponseTime, time.Since(startAt).Seconds())

		if err != nil {
			lastErr = err
			done(ctx, selector.DoneInfo{Err: err})
			reqOpt.UpstreamStatusCode = append(reqOpt.UpstreamStatusCode, 0)

			// 标记节点为不健康
			if c.healthCheck != nil {
				c.healthCheck.MarkUnhealthy(n)
				LOG.Warnf("Marked node as unhealthy after request failure: %s, error: %v", addr, err)
			}

			LOG.Warnf("Request failed (attempt %d/%d) to %s: %v", attempt+1, c.maxRetries, addr, err)
			continue
		}

		// 请求成功
		reqOpt.UpstreamStatusCode = append(reqOpt.UpstreamStatusCode, resp.StatusCode)
		reqOpt.DoneFunc = done
		return resp, nil
	}

	// 所有重试都失败
	if lastDone != nil {
		lastDone(ctx, selector.DoneInfo{Err: lastErr})
	}
	reqOpt.UpstreamStatusCode = append(reqOpt.UpstreamStatusCode, 0)
	return nil, lastErr
}
