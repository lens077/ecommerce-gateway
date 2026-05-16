# Gateway 主动健康检查功能

## 概述

网关实现了主动健康检查机制，用于实时检测后端服务的健康状态，当 Pod 重启导致 IP 变更时，能够及时发现并切换到新的服务实例。

## 核心功能

### 1. 主动健康检查
- 定期向后端节点发送 `/healthz` 请求
- 支持配置检查间隔、超时时间和失败阈值
- 自动标记不健康节点并从负载均衡池中移除

### 2. 智能节点过滤
- 请求路由时自动过滤不健康节点
- 节点恢复后自动重新加入可用节点池

### 3. 自动重试机制
- 请求失败时自动重试到健康节点
- 失败后自动标记节点为不健康

## 工作流程

```
┌─────────────────────────────────────────────────────────────────┐
│                    健康检查工作流程                              │
├─────────────────────────────────────────────────────────────────┤
│  1. Pod 重启 → IP 变更 → Consul 更新服务列表                     │
│                         ↓                                      │
│  2. 网关监听器收到更新 → 更新健康检查器节点列表                   │
│                         ↓                                      │
│  3. 健康检查器定期检查所有节点（默认 10s 间隔）                    │
│                         ↓                                      │
│  4. 请求路由 → 过滤不健康节点 → 选择健康节点                     │
│                         ↓                                      │
│  5. 请求失败 → 标记节点不健康 → 自动重试到其他节点               │
│                         ↓                                      │
│  6. 节点恢复 → 健康检查通过 → 重新加入可用节点池                 │
└─────────────────────────────────────────────────────────────────┘
```

## 配置说明

### 健康检查配置

| 配置项 | 默认值 | 说明 |
|--------|--------|------|
| `enableHealthCheck` | true | 是否启用健康检查 |
| `healthCheckInterval` | 10s | 健康检查间隔时间 |
| `healthCheckTimeout` | 2s | 单次检查超时时间 |
| `maxHealthCheckRetries` | 3 | 最大失败次数（超过此值标记为不健康） |
| `maxRetries` | 3 | 请求最大重试次数 |

### 配置示例

```go
factory := client.NewFactory(
    discovery,
    client.WithHealthCheck(true),           // 启用健康检查
    client.WithHealthCheckInterval(10*time.Second),
    client.WithHealthCheckTimeout(2*time.Second),
)
```

## 文件结构

```
gateway/client/
├── health_checker.go    # 健康检查器核心实现
├── client.go            # 客户端实现（集成健康检查）
├── factory.go           # 客户端工厂（配置选项）
└── servicewatch.go      # 服务监听（节点更新）
```

## 核心组件

### HealthChecker 接口

```go
type HealthChecker interface {
    Start()                              // 启动健康检查
    Stop()                               // 停止健康检查
    IsHealthy(node selector.Node) bool   // 检查节点是否健康
    MarkUnhealthy(node selector.Node)    // 标记节点为不健康
    HealthyNodeFilter() func(selector.Node) bool  // 获取健康节点过滤器
    updateNodes(nodes []selector.Node)   // 更新节点列表
}
```

### 健康检查器实现

- **定时检查**：使用 `time.Ticker` 定期执行健康检查
- **并发检查**：每个节点独立 goroutine 检查，互不阻塞
- **状态管理**：维护节点健康状态和失败计数
- **自动恢复**：节点恢复健康后自动重置状态

## 与 Consul 的协同

### 服务发现流程

1. **服务注册**：后端服务启动时注册到 Consul
2. **服务监听**：网关通过 `servicewatch` 监听 Consul 服务变化
3. **节点更新**：收到服务更新时同步更新健康检查器节点列表
4. **健康检查**：对新节点立即开始健康检查

### 处理场景

| 场景 | 处理方式 |
|------|----------|
| Pod 重启导致 IP 变更 | 健康检查器检测到旧节点不可达，自动标记为不健康 |
| 新 Pod 注册 | 自动加入健康检查，通过后加入可用节点池 |
| 服务降级 | 自动从负载均衡池中移除，避免请求失败 |
| 服务恢复 | 自动重新加入可用节点池 |

## 优势对比

| 特性 | 仅依赖 Consul TTL | 主动健康检查 |
|------|------------------|--------------|
| 检测速度 | 依赖 TTL 过期时间（约 1 分钟） | 即时检测（配置间隔） |
| 失败响应 | 等待服务标记为 Critical | 立即标记为不健康 |
| 用户体验 | 可能返回错误 | 自动重试到健康节点 |
| 可靠性 | 依赖外部服务 | 主动监控 |

## 最佳实践

### 1. 配置建议

```go
// 生产环境推荐配置
factory := client.NewFactory(
    discovery,
    client.WithHealthCheck(true),
    client.WithHealthCheckInterval(5*time.Second),   // 缩短检查间隔
    client.WithHealthCheckTimeout(1*time.Second),    // 缩短超时时间
    client.WithMaxRetries(5),                        // 增加重试次数
)
```

### 2. 后端服务要求

后端服务需要实现 `/healthz` 健康检查端点：

```go
func (s *Server) HealthCheck(ctx context.Context, req *health.CheckRequest) (*health.CheckResponse, error) {
    return &health.CheckResponse{
        Status: health.CheckResponse_SERVING,
    }, nil
}
```

### 3. 监控建议

监控以下指标：
- 健康检查成功率
- 节点健康状态变化
- 请求重试次数
- 节点切换频率

## 故障排除

### 问题：网关仍然使用旧 IP

**可能原因**：

1. **健康检查未启用**：检查 `enableHealthCheck` 配置
2. **检查间隔过长**：缩短 `healthCheckInterval`
3. **失败阈值过高**：降低 `maxHealthCheckRetries`
4. **健康检查端点未实现**：确保后端服务实现 `/healthz`

**排查命令**：

```bash
# 查看网关日志
kubectl logs -f <gateway-pod> | grep "health"

# 检查后端健康状态
curl http://<backend-service>/healthz
```

## 总结

主动健康检查功能为网关提供了实时的后端服务状态监控能力，能够在 Pod 重启、IP 变更等场景下及时发现并切换到健康节点，大大提升了系统的可用性和稳定性。
