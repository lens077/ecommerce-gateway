package constants

import "time"

// 服务发现相关常量
const (
	// DiscoveryInitTimeout 服务发现初始化超时时间
	DiscoveryInitTimeout = 10 * time.Second

	// DiscoveryMaxRetryDelay 服务发现最大重试延迟
	DiscoveryMaxRetryDelay = 60 * time.Second

	// DiscoveryLogThreshold 日志输出阈值（失败次数）
	DiscoveryLogThreshold = 3

	// DiscoveryCleanupInterval 清理间隔
	DiscoveryCleanupInterval = 30 * time.Second
)

// DiscoveryRetrySteps 服务发现重试退避阶梯
var DiscoveryRetrySteps = []time.Duration{
	1 * time.Second,
	10 * time.Second,
	30 * time.Second,
	60 * time.Second,
}