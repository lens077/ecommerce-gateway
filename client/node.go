package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-kratos/kratos/v2/selector"
	"github.com/prometheus/client_golang/prometheus"
	"golang.org/x/net/http2"

	config "github.com/go-kratos/gateway/api/gateway/config/v1"
	"github.com/go-kratos/gateway/middleware"
)

var (
	_                selector.Node = &node{}
	_globalClient                  = defaultClient()
	_globalH2Client                = defaultH2Client()
	_globalH2CClient               = defaultH2CClient()
	_dialTimeout                   = 200 * time.Millisecond
	followRedirect                 = false
)

func init() {
	var err error
	if v := os.Getenv("PROXY_DIAL_TIMEOUT"); v != "" {
		if _dialTimeout, err = time.ParseDuration(v); err != nil {
			panic(err)
		}
	}
	if val := os.Getenv("PROXY_FOLLOW_REDIRECT"); val != "" {
		followRedirect = true
	}
	prometheus.MustRegister(_metricClientRedirect)
}

var _metricClientRedirect = prometheus.NewCounterVec(prometheus.CounterOpts{
	Namespace: "go",
	Subsystem: "gateway",
	Name:      "client_redirect_total",
	Help:      "The total number of client redirect",
}, []string{"protocol", "method", "path", "service", "basePath"})

func defaultCheckRedirect(req *http.Request, via []*http.Request) error {
	labels, ok := middleware.MetricsLabelsFromContext(req.Context())
	if ok {
		_metricClientRedirect.WithLabelValues(labels.Protocol(), labels.Method(), labels.Path(), labels.Service(), labels.BasePath()).Inc()
	}
	if followRedirect {
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return http.ErrUseLastResponse
}

func defaultClient() *http.Client {
	return &http.Client{
		CheckRedirect: defaultCheckRedirect,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   _dialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          10000,
			MaxIdleConnsPerHost:   1000,
			MaxConnsPerHost:       1000,
			DisableCompression:    true,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
}

func defaultH2Client() *http.Client {
	// 1. 创建标准的 HTTP/2 Transport
	t2 := &http2.Transport{
		AllowHTTP:          true, // 关键：允许处理 http:// 协议
		DisableCompression: true,
		// 使用自定义 Dialer 覆盖 DialTLSContext
		DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout:   _dialTimeout,
				KeepAlive: 30 * time.Second,
			}

			// 如果 cfg 为 nil，说明是 http:// 请求 (h2c)
			fmt.Printf("cfg:%+v", cfg)
			if cfg == nil {
				return dialer.DialContext(ctx, network, addr)
			}

			// 如果 cfg 不为 nil，说明是 https:// 请求，需要手动建立 TLS 连接
			rawConn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}

			// 包装成 TLS 连接
			conf := cfg.Clone()
			if len(conf.NextProtos) == 0 {
				conf.NextProtos = []string{"h2", "http/1.1"}
			}

			tlsConn := tls.Client(rawConn, conf)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				rawConn.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	}

	return &http.Client{
		CheckRedirect: defaultCheckRedirect,
		Transport:     t2,
	}
}

// 专门用于处理 H2C (明文 HTTP/2)
func defaultH2CClient() *http.Client {
	t2 := &http2.Transport{
		AllowHTTP:          true,
		DisableCompression: true,
		// 对于 H2C，忽略cfg，使用普通 TCP 拨号
		DialTLSContext: func(ctx context.Context, network, addr string, cfg *tls.Config) (net.Conn, error) {
			dialer := &net.Dialer{
				Timeout:   _dialTimeout,
				KeepAlive: 30 * time.Second,
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}

	return &http.Client{
		CheckRedirect: defaultCheckRedirect,
		Transport:     t2,
	}
}

func newNode(addr string, protocol config.Protocol, weight *int64, md map[string]string, version string, name string) *node {
	node := &node{
		protocol: protocol,
		address:  addr,
		weight:   weight,
		metadata: md,
		version:  version,
		name:     name,
	}

	if protocol == config.Protocol_GRPC {
		// 区分 HTTPS 和 HTTP (H2C)
		// 如果地址明确是 https 开头，使用 TLS 客户端
		// 例如localhost:4000 这种不带 scheme 的情况都默认为 H2C
		if strings.HasPrefix(addr, "https://") {
			node.client = _globalH2Client
		} else {
			node.client = _globalH2CClient
		}
	} else {
		node.client = _globalClient
	}
	return node
}

type node struct {
	address  string
	name     string
	weight   *int64
	version  string
	metadata map[string]string

	client   *http.Client
	protocol config.Protocol
}

func (n *node) Scheme() string {
	return strings.ToLower(n.protocol.String())
}

func (n *node) Address() string {
	return n.address
}

// ServiceName is service name
func (n *node) ServiceName() string {
	return n.name
}

// InitialWeight is the initial value of scheduling weight
// if not set return nil
func (n *node) InitialWeight() *int64 {
	return n.weight
}

// Version is service node version
func (n *node) Version() string {
	return n.version
}

// Metadata is the kv pair metadata associated with the service instance.
// version,namespace,region,protocol etc..
func (n *node) Metadata() map[string]string {
	return n.metadata
}
