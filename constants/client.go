package constants

import "time"

// 健康检查相关常量
const (
	// HealthCheckInterval 健康检查间隔
	HealthCheckInterval = 10 * time.Second

	// HealthCheckTimeout 健康检查超时时间
	HealthCheckTimeout = 2 * time.Second

	// HealthCheckDialTimeout 健康检查连接超时
	HealthCheckDialTimeout = 500 * time.Millisecond

	// HealthCheckKeepAlive 健康检查连接保活时间
	HealthCheckKeepAlive = 30 * time.Second
)

// 客户端连接相关常量
const (
	// ClientDialTimeout 客户端连接超时
	ClientDialTimeout = 200 * time.Millisecond

	// ClientKeepAlive 客户端连接保活时间
	ClientKeepAlive = 30 * time.Second

	// ClientIdleConnTimeout 客户端空闲连接超时
	ClientIdleConnTimeout = 90 * time.Second

	// ClientTLSHandshakeTimeout 客户端TLS握手超时
	ClientTLSHandshakeTimeout = 10 * time.Second

	// ClientExpectContinueTimeout 客户端Expect-Continue超时
	ClientExpectContinueTimeout = 1 * time.Second

	// ClientRefreshInterval 客户端服务列表刷新间隔
	ClientRefreshInterval = 15 * time.Second
)