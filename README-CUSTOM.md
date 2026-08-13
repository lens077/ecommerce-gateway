# 自建 API 网关

> 专门为电商微服务架构设计的高性能、可扩展的 API 网关，基于 Go-Kratos 框架构建。

## 目录

- [诞生背景](#诞生背景)
- [解决的问题](#解决的问题)
- [核心特性](#核心特性)
- [架构设计](#架构设计)
- [配置说明](#配置说明)
- [与 Traefik 的对比](#与-traefik-的对比)
- [快速开始](#快速开始)

## 诞生背景

这个网关是为了解决电商微服务系统在实际生产环境中遇到的具体问题而构建的：

1. **协议转换需求**：电商后端服务大量使用 gRPC，但前端需要 HTTP/JSON 接口
2. **统一认证授权**：需要集中处理 JWT 认证和 RBAC 权限控制
3. **服务发现集成**：深度集成 Consul，支持动态服务发现和健康检查
4. **配置热更新**：网关路由、中间件配置需要支持动态更新而无需重启
5. **与 Casdoor 深度集成**：需要与现有的 Casdoor 用户认证系统无缝对接
6. **多协议支持**：同时支持 HTTP/1.1、HTTP/2、HTTP/3 (QUIC)

## 解决的问题

### 1. 微服务统一入口
- 将多个微服务的访问统一到单一入口
- 简化客户端调用，无需感知后端服务的具体地址

### 2. 跨域处理
- 开箱即用的 CORS 支持
- 灵活的跨域配置，支持多前端应用同时接入

### 3. 认证授权集中化
- JWT 认证集中处理
- RBAC 基于角色的访问控制
- 与 Casdoor 身份认证系统深度集成

### 4. 服务可靠通信
- 自动熔断降级
- 智能重试机制
- 健康检查与服务发现集成

### 5. 可观测性
- 请求链路追踪 (Tracing)
- 详细的访问日志
- Prometheus 指标收集

## 核心特性

### 🔄 服务发现与负载均衡

**实现文件**：[discovery/consul/consul.go](./discovery/consul/consul.go)

- 深度集成 Consul 服务发现
- 支持两种模式：
  - **服务发现模式**：`discovery:///service-name` - 通过 Consul 发现服务实例，自动负载均衡
  - **直连模式**：`direct://host:port` - 直接连接到指定地址，性能最优
- 健康检查自动感知，自动剔除不健康实例
- 支持权重、区域感知等高级负载均衡策略

### 🛡️ JWT 认证中间件

**实现文件**：[middleware/jwt/jwt.go](./middleware/jwt/jwt.go)

- 基于 RSA 公钥验证的 JWT 认证
- 支持配置热更新（JWT 公钥自动重新加载）
- 灵活的路由白名单配置（`router_filter`）
- 自动从 Casdoor 获取用户信息
- 支持令牌过期检测

### 🚦 RBAC 权限控制

**实现文件**：[middleware/rbac/rbac.go](./middleware/rbac/rbac.go)

- 基于 Casbin 的权限模型
- 支持 Redis 策略缓存（可扩展）
- 与 Casdoor 角色系统集成
- 支持路径参数匹配和通配符规则
- 策略和模型支持 Config Center 热更新
- 本地缓存加速权限验证

### 🔌 可插拔的中间件系统

支持的中间件（11 个，链顺序以 `configs/config.yaml` 为准）：
- **IP** - 客户端真实 IP 提取（链路第一层）
- **CORS** - 跨域资源共享
- **JWT** - JWT 令牌验证
- **RBAC** - 基于角色的访问控制
- **Tracing** - 分布式链路追踪
- **Logging** - 访问日志记录
- **Circuit Breaker** - 熔断器
- **BBR** - 基于 BBR 算法的流量控制
- **Router Filter** - 路由过滤器
- **Rewrite** - URL 重写
- **Transcoder** - 协议转换

### ⚡ 请求处理流程

**核心流程**：[proxy/proxy.go](./proxy/proxy.go)

```
HTTP 请求 
  ↓
路由器匹配 (Router)
  ↓
中间件链 (Middleware Chain)
  ↓
服务选择 (Service Discovery)
  ↓
负载均衡 (Load Balancing)
  ↓
HTTP 客户端 (Client)
  ↓
重试机制 (Retry)
  ↓
后端服务
```

### 🔄 配置热更新

- 支持从独立 Config Center 动态加载配置
- 显式本地文件模式支持路由配置轮询
- 路由规则、中间件配置零重启更新
- JWT 公钥、RBAC 策略文件自动重新加载
- 配置优先级：本地优先级目录覆盖 Config Center 路由配置
- Config Center Watch 断线后按 1s～30s 指数退避重连；空值、删除或无效更新保留上一份可用配置

正常启动由 `CONFIG_SOURCE_FILE` 指向本地 selector，selector 定位同一 namespace/environment 下的
四个条目：`config.yaml`、`secrets/public.pem`、`policies/policies.csv`、`policies/model.conf`。
四个条目必须设置 `is_secret=false`，否则机器 token 读取时只会得到脱敏占位值，网关无法解析；
selector 内的机器 token 与 TLS 私钥仍只放本地或 Kubernetes Secret。Consul 只负责服务发现，
不存在 Consul KV 配置回退。示例见 [configs/source.yaml.example](configs/source.yaml.example)。

### 📊 可观测性

- Prometheus 指标：请求数、延迟、字节数、重试次数
- 分布式链路追踪（支持 OpenTelemetry）
- 详细的访问日志，包含用户信息、请求/响应大小、延迟等
- 健康检查端点

### 🌐 多协议支持

- HTTP/1.1 明文
- HTTP/2 明文 (h2c)
- HTTP/2 over TLS
- HTTP/3 (QUIC) 支持
- TLS 配置热更新
- 支持同时监听 TCP 和 UDP 端口

## 架构与配置（指针）

本章原有的「架构设计」「配置说明」与 `README.md`、交互式架构图重复且已漂移（中间件链顺序写错、
路径过时），2026-08-13 删除。现行入口：

- 架构与 9 层中间件链实际顺序：根仓 `docs/architecture/ecommerce-gateway.html`（交互式架构图）
  + [docs/REQUEST_FLOW.md](docs/REQUEST_FLOW.md)（带代码锚点的文字版全链路）
- 配置说明与环境变量表：[README.md](README.md)；Config Center 条目模板以 [configs/config.yaml](configs/config.yaml) 为准

## 与 Traefik 的对比

| 特性 | 自建网关 | Traefik |
|------|---------|---------|
| **诞生目的** | 专门为电商微服务设计的定制化网关 | 通用型云原生反向代理和负载均衡器 |
| **配置方式** | YAML + Config Center Watch，支持深度定制 | 声明式配置，多种提供商支持 |
| **JWT 认证** | 内置、与 Casdoor 深度集成、公钥热更新 | 通过插件支持，但需要额外配置 |
| **RBAC 权限** | 内置 Casbin + Redis，与 Casdoor 角色联动 | 需要通过插件或自定义中间件实现 |
| **服务发现** | Consul 深度集成、健康检查联动 | 支持多种服务发现 (K8s, Consul, etcd) |
| **协议支持** | HTTP/1.1, HTTP/2, HTTP/3, gRPC | HTTP/1.1, HTTP/2, HTTP/3, TCP/UDP |
| **配置热更新** | 完全支持，包括路由、中间件、JWT、RBAC | 支持，但某些配置需要重启 |
| **可观测性** | Prometheus 指标、链路追踪、详细日志 | 内置指标、访问日志，可扩展 |
| **熔断器/限流** | 内置、灵活配置 | 通过插件支持 |
| **部署复杂度** | 需要 Golang 环境、独立部署 | 容器优先，易于部署 |
| **性能** | 高度优化，Go 原生 | 高性能，但可能有额外抽象开销 |
| **适用场景** | 电商微服务、需要深度定制认证授权的场景 | 通用云原生应用、API 网关、Ingress |

### 为什么选择自建网关而不是 Traefik？

1. **深度集成需求**：与现有 Casdoor、Config Center、Consul 服务发现深度集成
2. **定制化认证逻辑**：需要实现特定的 JWT + RBAC 认证流程
3. **电商业务特性**：针对电商场景的特殊路由规则和鉴权需求
4. **配置热更新**：需要更细粒度的配置热更新控制
5. **团队技术栈**：后端团队主要使用 Go，便于维护和扩展
6. **协议转换需求**：HTTP ↔ gRPC 协议转换的深度定制

## 运行

见 [README.md](README.md) 环境变量表与根仓 README「运行 · 网关」。
（原「快速开始」章 2026-08-13 删除：`configs/config.yaml.example`、`make docker-build`、
`deploy/dev/docker-compose.yml`、`curl /healthz` 均不存在——网关自身不暴露 healthz，
`/healthz` 只是网关探测后端的路径。前置要求同根仓：Go 1.26+、Config Center、Consul 服务发现。）

## 项目结构

```
gateway/
├── api/                    # API 定义 (proto)
│   └── gateway/
├── client/                 # 客户端实现
├── cmd/
│   └── gateway/
│       └── main.go        # 主入口
├── config/                 # 配置加载
├── deploy/                 # 部署文件
├── discovery/              # 服务发现
│   └── consul/
├── examples/              # 示例代码
├── infrastructure/        # 基础设施
├── middleware/            # 中间件（11 个）
│   ├── bbr/              # 流量控制
│   ├── circuitbreaker/   # 熔断器
│   ├── cors/             # 跨域
│   ├── ip/               # 客户端真实 IP（链路第一层）
│   ├── jwt/              # JWT 认证
│   ├── logging/          # 日志
│   ├── rbac/             # 权限控制
│   ├── rewrite/          # 路径改写
│   ├── routerfilter/     # 路由过滤
│   ├── tracing/          # 链路追踪
│   └── transcoder/       # 协议转换
├── pkg/                  # 工具包
├── proxy/                # 代理核心
├── router/               # 路由器
├── server/               # 服务器
└── third_party/          # 第三方依赖
```

## 许可证

MIT License

## 贡献

欢迎提交 Issue 和 PR！

---

**注意**：此网关是为特定的电商微服务架构设计的，如果您有类似的需求可以参考，如果是通用场景，Traefik、APISIX 或 Envoy 可能是更好的选择。
