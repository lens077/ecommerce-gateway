package consul

import (
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/go-kratos/gateway/constants"
	"github.com/go-kratos/gateway/discovery"
	"github.com/go-kratos/kratos/contrib/registry/consul/v2"
	"github.com/go-kratos/kratos/v2/registry"
	"github.com/hashicorp/consul/api"
)

func init() {
	discovery.Register("consul", New)
}

func New(dsn *url.URL) (registry.Discovery, error) {
	c := api.DefaultConfig()

	c.Address = dsn.Host
	
	// 设置 Consul 阻塞查询的最大等待时间
	// 这个设置影响服务发现的更新频率，较短的时间可以更快地获取服务变化
	c.WaitTime = 10 * time.Second
	
	// 从环境变量读取默认值
	if scheme := os.Getenv(constants.ConsulScheme); scheme != "" {
		c.Scheme = scheme
	} else if strings.HasSuffix(dsn.Host, ":443") {
		// 如果端口是 443，默认使用 https
		c.Scheme = "https"
	} else {
		c.Scheme = "http"
	}
	
	// 从环境变量读取 TLS 配置
	insecureSkipVerify := false
	if insecureStr := os.Getenv(constants.ConsulInsecureSkipVerify); insecureStr != "" {
		insecureSkipVerify = insecureStr == "true"
	} else if c.Scheme == "https" {
		// 如果使用 https，默认禁用证书验证（开发环境常见自签名证书）
		insecureSkipVerify = true
	}
	
	if insecureSkipVerify {
		c.TLSConfig = api.TLSConfig{
			InsecureSkipVerify: true,
		}
	}
	
	// 从 URL 查询参数或环境变量读取 Token
	token := dsn.Query().Get("token")
	if token == "" {
		token = os.Getenv(constants.ConsulToken)
	}
	if token != "" {
		c.Token = token
	}
	
	// 从 URL 查询参数或环境变量读取 Datacenter
	datacenter := dsn.Query().Get("datacenter")
	if datacenter == "" {
		datacenter = os.Getenv(constants.ConsulDatacenter)
	}
	if datacenter != "" {
		c.Datacenter = datacenter
	}
	
	client, err := api.NewClient(c)
	if err != nil {
		return nil, err
	}
	return consul.New(client), nil
}
