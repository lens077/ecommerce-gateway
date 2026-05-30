# 网关架构演进策略：Cilium 边缘网关 + 电商 BFF 层

> 从当前的"全能型"自建网关，演进为 Cilium 作为边缘网关负责通用能力 + 自建网关专注于电商 BFF 的分层架构

## 目录

- [演进目标](#演进目标)
- [架构对比](#架构对比)
- [分阶段实施路线](#分阶段实施路线)
- [Cilium 配置方案](#cilium-配置方案)
- [自建网关 BFF 转型](#自建网关-bff-转型)
- [迁移验证与回滚](#迁移验证与回滚)

---

## 演进目标

### 📋 当前架构问题
- [gateway/main.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/cmd/gateway/main.go) 职责过重：从 SSL 终结到业务路由都在处理
- 维护成本高：通用基础设施能力需要电商团队维护
- 能力重复：认证、授权、路由等都是通用需求
- 可观测性分散：指标、日志、追踪没有统一标准

### 🎯 未来架构愿景

```
                    ┌─────────────────────────────────┐
                    │         外部流量              │
                    └────────────────┬────────────────┘
                                     │
                    ┌────────────────▼────────────────┐
                    │     Cilium 边缘网关 (L7)       │
                    │  ┌───────────────────────────┐  │
                    │  │ SSL/TLS 终结            │  │
                    │  │ 通用认证 (OAuth2/JWT)    │  │
                    │  │ 基础路由 & 负载均衡      │  │
                    │  │ 限流 & 熔断            │  │
                    │  │ 可观测性统一采集        │  │
                    │  └───────────────────────────┘  │
                    └────────────────┬────────────────┘
                                     │
                    ┌────────────────▼────────────────┐
                    │    电商 BFF (自建网关)        │
                    │  ┌───────────────────────────┐  │
                    │  │ 电商业务聚合 API        │  │
                    │  │ 页面级数据聚合          │  │
                    │  │ 协议转换 (gRPC → HTTP)   │  │
                    │  │ 业务特定的权限验证      │  │
                    │  │ Cache & 个性化处理     │  │
                    │  └───────────────────────────┘  │
                    └────────────────┬────────────────┘
                                     │
            ┌────────────────────────┼────────────────────────┐
            │                        │                        │
   ┌────────▼───────┐      ┌────────▼───────┐      ┌────────▼───────┐
   │  User Service  │      │ Product Service │      │  Order Service  │
   │  (gRPC/HTTP)   │      │  (gRPC/HTTP)   │      │  (gRPC/HTTP)   │
   └────────────────┘      └────────────────┘      └────────────────┘
```

### ✨ 预期收益

1. **降低维护成本**
   - 通用能力由云原生团队维护
   - 业务团队专注于业务价值
   - 减少重复造轮子

2. **提升稳定性**
   - Cilium 经过生产验证，社区活跃
   - 边缘与业务隔离，故障影响范围缩小
   - 边缘能力统一升级

3. **保留灵活性**
   - 业务特定的需求仍可在 BFF 层定制
   - 电商协议转换、页面聚合保留在自建网关
   - 业务逻辑演进不受边缘约束

4. **标准化可观测性**
   - 统一指标、日志、追踪标准
   - 降低排查难度
   - 提升运维效率

---

## 架构对比

### 当前架构（自建网关承担所有责任）

| 能力 | 当前位置 | 代码位置 |
|------|----------|---------|
| SSL/TLS 终结 | 自建网关 | [main.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/cmd/gateway/main.go#L168) |
| JWT 认证 | 自建网关 | [middleware/jwt/jwt.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/jwt/jwt.go) |
| RBAC 授权 | 自建网关 | [middleware/rbac/rbac.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/rbac/rbac.go) |
| 服务发现路由 | 自建网关 | [discovery/consul/consul.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/discovery/consul/consul.go) |
| 协议转换 | 自建网关 | [proxy/proxy.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/proxy/proxy.go) |
| 业务聚合 | 无 | - |

### 未来架构（能力分层）

| 能力 | 位置 | 说明 |
|------|------|------|
| SSL/TLS 终结 | **Cilium** | 统一证书管理，硬件加速 |
| JWT 认证 | **Cilium** | OAuth2 代理，可与 Casdoor 集成 |
| RBAC 基础授权 | **Cilium** | EnvoyFilter 实现路径级授权 |
| 基础路由 & LB | **Cilium** | 基于服务发现的动态路由 |
| 限流 & 熔断 | **Cilium** | Envoy 原生能力 |
| 可观测性 | **Cilium** | 统一指标、追踪采集 |
| 业务协议转换 | **自建 BFF** | gRPC ↔ HTTP，Connect 协议支持 |
| 页面级数据聚合 | **自建 BFF** | 聚合多个微服务数据 |
| 业务特定权限 | **自建 BFF** | 电商场景细粒度权限 |
| 个性化 & Cache | **自建 BFF** | 用户画像相关处理 |

---

## 分阶段实施路线

### 🎯 阶段 0：现状评估与准备（2-3周）

#### 目标
- 完成能力边界梳理
- 准备 Cilium 环境
- 制定详细迁移计划

#### 任务清单
- [ ] **当前网关能力审计**
  - 审计 [middleware/](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/) 所有中间件
  - 记录哪些是通用的，哪些是电商特定的
  - 分析 [config.yaml.example](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/configs/config.yaml.example) 的所有配置项

- [ ] **Cilium 环境搭建**
  - Kubernetes 环境中安装 Cilium
  - 启用 Hubble 可观测性
  - 验证 Cilium 与现有 Consul 服务发现集成

- [ ] **回滚计划制定**
  - 确定流量切换开关
  - 准备降级策略

#### 产出物
- 《网关能力拆分清单》
- 《Cilium 环境验证报告》
- 《迁移与回滚计划》

---

### 🚀 阶段 1：Cilium 上线，灰度验证（3-4周）

#### 目标
- Cilium 作为边缘网关上线
- 验证 SSL 终结、基础路由能力
- 完成 10% 流量灰度验证

#### 架构图
```
外部流量 ──┬─────> 当前自建网关（100% 流量）
            │
            └─────> Cilium（验证环境，内部测试）
                        │
                        └─────> 自建网关（复用）
```

#### Cilium 配置重点

**1. Gateway API 配置**

```yaml
# gateway.yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: Gateway
metadata:
  name: ecommerce-gateway
  namespace: kube-system
spec:
  gatewayClassName: cilium
  listeners:
    - name: https
      port: 443
      protocol: HTTPS
      hostname: "api.example.com"
      tls:
        certificateRefs:
          - name: ecommerce-tls
    - name: http
      port: 80
      protocol: HTTP
```

**2. HTTPRoute 配置（基础路由）**

```yaml
# httproute.yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: ecommerce-routes
spec:
  parentRefs:
    - name: ecommerce-gateway
  rules:
    - matches:
        - path:
            type: PathPrefix
            value: /user
      filters:
        - type: RequestHeaderModifier
          requestHeaderModifier:
            add:
              - name: X-From-Cilium
                value: "true"
      backendRefs:
        - name: legacy-gateway
          port: 8080
    - matches:
        - path:
            type: PathPrefix
            value: /product
      backendRefs:
        - name: legacy-gateway
          port: 8080
```

**3. EnvoyFilter（为后续 JWT 认证做准备）**

```yaml
# envoyfilter-jwt.yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: jwt-authn
spec:
  configPatches:
    - applyTo: HTTP_FILTER
      match:
        context: GATEWAY
      patch:
        operation: INSERT_BEFORE
        value:
          name: envoy.filters.http.jwt_authn
          typed_config:
            "@type": "type.googleapis.com/envoy.extensions.filters.http.jwt_authn.v3.JwtAuthentication"
            providers:
              casdoor:
                issuer: "https://casdoor.example.com"
                local_jwks:
                  inline_string: "{...}"
                payload_in_metadata: "jwt_payload"
            rules:
              - match:
                  prefix: /
                requires: { provider_name: "casdoor" }
```

#### 自建网关改造（最小改动）
- 保持现有代码不变
- 只修改配置，监听来自 Cilium 的 `X-From-Cilium` Header
- 更新 [config/config.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/config/config.go)，添加 Cilium 模式开关

#### 验证指标
- SSL 握手成功率 > 99.9%
- P99 延迟增加 < 5ms
- 无业务错误增加

---

### 🔄 阶段 2：逐步迁移认证能力到 Cilium（3-4周）

#### 目标
- JWT 认证迁移到 Cilium
- 保持 RBAC 在自建网关（因与业务强相关）
- 50% 流量走新链路

#### 能力边界重新定义

| 能力 | Cilium | 自建 BFF |
|------|--------|----------|
| JWT Token 验证 | ✅ | - |
| Token 过期检查 | ✅ | - |
| 用户 ID 透传 | ✅（Header） | - |
| RBAC 权限检查 | - | ✅（保留） |
| 业务细粒度权限 | - | ✅ |

#### Cilium 完整认证配置

**更新 HTTPRoute 以包含 JWT 验证**

```yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: ecommerce-routes
spec:
  parentRefs:
    - name: ecommerce-gateway
  rules:
    # 公开接口，无需认证
    - matches:
        - path:
            type: PathPrefix
            value: /user/user.v1.UserService/SignIn
      backendRefs:
        - name: legacy-gateway
          port: 8080
    # 需要认证的接口
    - matches:
        - path:
            type: PathPrefix
            value: /
      filters:
        - type: RequestHeaderModifier
          requestHeaderModifier:
            add:
              - name: X-User-Id
                value: "{jwt_payload.sub}"
              - name: X-User-Role
                value: "{jwt_payload.role}"
      backendRefs:
        - name: legacy-gateway
          port: 8080
```

**自建网关改动（移除 JWT 验证）**

修改 [middleware/jwt/jwt.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/jwt/jwt.go)：

```go
// 新的 JWT 中间件：只从 Header 读取，不验证
func Middleware(c *config.Middleware) (middleware.Middleware, error) {
    return func(next http.RoundTripper) http.RoundTripper {
        return middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
            // 直接从 Cilium 设置的 Header 读取
            userID := req.Header.Get("X-User-Id")
            role := req.Header.Get("X-User-Role")
            
            if userID != "" {
                req.Header.Set(constants.UserIdMetadataKey, userID)
                req.Header.Set(constants.UserRoleMetadataKey, role)
            }
            
            return next.RoundTrip(req)
        })
    }, nil
}
```

#### 灰度策略
- 按用户 ID 灰度（10% → 30% → 50%）
- 或者按业务域灰度（先 product，再 order，最后 user）

---

### 🏗️ 阶段 3：自建网关转型为 BFF（4-6周）

#### 目标
- 移除通用路由能力，专注于业务聚合
- 实现页面级 API 聚合
- 100% 流量迁移完成

#### 新的自建网关架构

```
gateway/                      # 重命名为 ecommerce-bff
├── api/                     # 业务聚合 API 定义
│   ├── homepage/            # 首页聚合 API
│   ├── product-detail/      # 商品详情页 API
│   └── checkout/            # 结算页 API
├── pkg/
│   ├── aggregator/          # 数据聚合逻辑
│   │   ├── homepage.go
│   │   └── product.go
│   └── protocol/            # 协议转换（保留）
│       └── grpc_http.go
├── middleware/              # 保留业务特定的
│   ├── rbac/                # 保留
│   └── circuitbreaker/      # 业务特定熔断
└── cmd/
    └── bff/                 # 主程序
        └── main.go
```

#### 移除的组件
- [middleware/jwt/](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/jwt/) - JWT 验证已移到 Cilium
- [middleware/cors/](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/cors/) - CORS 由 Cilium 处理
- [middleware/logging/](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/logging/) - 可观测性统一到 Cilium
- [middleware/tracing/](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/tracing/) - 追踪统一到 Cilium

#### 新增的 BFF 能力

**页面级数据聚合示例**：

```go
// pkg/aggregator/homepage.go
package aggregator

type HomepageResponse struct {
    Banner     []Banner     `json:"banner"`
    Categories []Category   `json:"categories"`
    Products   []Product    `json:"products"`
    CartCount  int          `json:"cartCount"`
}

func (a *HomepageAggregator) Aggregate(ctx context.Context, userID string) (*HomepageResponse, error) {
    var wg sync.WaitGroup
    var resp HomepageResponse
    var err error
    
    // 并行调用多个微服务
    wg.Add(4)
    
    go func() {
        defer wg.Done()
        resp.Banner, err = bannerClient.GetBanners(ctx)
    }()
    
    go func() {
        defer wg.Done()
        resp.Categories, err = categoryClient.GetCategories(ctx)
    }()
    
    go func() {
        defer wg.Done()
        resp.Products, err = productClient.GetRecommendProducts(ctx, userID)
    }()
    
    go func() {
        defer wg.Done()
        resp.CartCount, err = cartClient.GetCartCount(ctx, userID)
    }()
    
    wg.Wait()
    return &resp, err
}
```

#### 新的路由配置（大幅简化）

```yaml
# BFF 配置
name: ecommerce-bff
endpoints:
  # 页面级聚合 API（新）
  - path: /api/homepage
    protocol: HTTP
    handler: aggregator.homepage
  - path: /api/product/{id}
    protocol: HTTP
    handler: aggregator.productDetail
  
  # 透传 API（兼容旧客户端）
  - path: /user*
    protocol: GRPC
    backends:
      - target: 'discovery:///user-identity-v1'
  - path: /product*
    protocol: GRPC
    backends:
      - target: 'discovery:///product-core-v1'
```

---

### ✅ 阶段 4：清理与优化（2-3周）

#### 目标
- 移除旧网关的无用代码
- 完善 BFF 能力
- 完成文档更新

#### 清理清单
- [ ] 移除 [middleware/jwt/](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/jwt/jwt.go) 的验证逻辑
- [ ] 移除 TLS 相关配置
- [ ] 简化 [config/](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/config/) 模块
- [ ] 更新 README 文档

---

## Cilium 配置方案

### 完整配置清单

#### 1. Cilium 安装配置

```yaml
# values.yaml - Helm 配置
cilium:
  gatewayAPI:
    enabled: true
  hubble:
    enabled: true
    metrics:
      enabled:
        - dns:query
        - drop
        - tcp
        - flow
        - port-distribution
        - icmp
        - http
  envoy:
    enabled: true
  l7Proxy: true
```

#### 2. SSL/TLS 配置

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: ecommerce-tls
type: kubernetes.io/tls
data:
  tls.crt: LS0tLS1CRUdJTi... # base64 encoded cert
  tls.key: LS0tLS1CRUdJTi... # base64 encoded key
```

#### 3. 认证策略（与 Casdoor 集成）

```yaml
apiVersion: security.policy.cilium.io/v1beta1
kind: CiliumNetworkPolicy
metadata:
  name: api-authn
spec:
  endpointSelector:
    matchLabels:
      app: ecommerce-bff
  ingress:
    - fromEntities:
        - world
      toPorts:
        - ports:
            - port: "8080"
              protocol: TCP
          rules:
            http:
              - method: POST
                path: "/user/user.v1.UserService/SignIn"
              - jwtRule:
                  issuer: "https://casdoor.example.com"
                  audiences:
                    - "ecommerce-app"
```

#### 4. 流量镜像策略（灰度验证用）

```yaml
apiVersion: gateway.networking.k8s.io/v1beta1
kind: HTTPRoute
metadata:
  name: canary-mirror
spec:
  rules:
    - backendRefs:
        - name: legacy-gateway
          port: 8080
          weight: 90
        - name: ecommerce-bff
          port: 8080
          weight: 10
```

---

## 自建网关 BFF 转型

### 代码变更指南

#### 1. 保留的核心能力

**必须保留**：
- [proxy/proxy.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/proxy/proxy.go) 中的协议转换
- [middleware/rbac/](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/rbac/rbac.go) 业务授权
- [middleware/circuitbreaker/](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/middleware/circuitbreaker/circuitbreaker.go) 业务熔断

#### 2. 新增 BFF 聚合层

**建议目录结构**：

```
gateway/
├── api/
│   └── bff/
│       └── v1/
│           ├── homepage.proto
│           └── checkout.proto
├── internal/
│   └── bff/
│       ├── aggregator/
│       │   ├── homepage.go
│       │   ├── product.go
│       │   └── checkout.go
│       └── service/
│           └── bff_service.go
└── cmd/
    └── bff/
        └── main.go
```

#### 3. BFF 服务示例

```go
// internal/bff/service/bff_service.go
package service

import (
    "context"
    "github.com/go-kratos/kratos/v2/log"
    
    "gateway/internal/bff/aggregator"
    pb "gateway/api/bff/v1"
)

type BFFService struct {
    pb.UnimplementedBFFServiceServer
    homepageAgg *aggregator.HomepageAggregator
    log *log.Helper
}

func (s *BFFService) GetHomepage(ctx context.Context, req *pb.GetHomepageRequest) (*pb.GetHomepageResponse, error) {
    data, err := s.homepageAgg.Aggregate(ctx, req.UserId)
    if err != nil {
        return nil, err
    }
    
    return &pb.GetHomepageResponse{
        Banner: toProtoBanners(data.Banner),
        Categories: toProtoCategories(data.Categories),
        Products: toProtoProducts(data.Products),
        CartCount: int32(data.CartCount),
    }, nil
}
```

#### 4. 简化后的 main.go

参考当前 [main.go](file:///Users/sumery/github/lens077/github/sunmery/ecommerce/gateway/cmd/gateway/main.go)，移除的部分：

```go
// ❌ 移除：JWT 初始化
// jwtErr := jwt.Init()

// ❌ 移除：TLS 服务器配置
// USE_TLS 相关代码

// ✅ 保留：RBAC 业务授权
rbacErr := rbac.InitEnforcer()

// ✅ 新增：BFF 服务注册
app := kratos.New(
    kratos.Name("ecommerce-bff"),
    kratos.Server(
        httpSrv,  // BFF HTTP 服务器
        grpcSrv,  // 如果需要 gRPC 服务
        server.NewProxy(serverHandler),  // 保留部分代理能力
    ),
)
```

---

## 迁移验证与回滚

### 验证清单

#### 功能验证
- [ ] 用户登录流程正常
- [ ] 认证后的 API 访问正常
- [ ] RBAC 权限控制生效
- [ ] 协议转换正常工作

#### 性能验证
- [ ] P99 延迟在可接受范围内（增加 < 20ms）
- [ ] 错误率 < 0.1%
- [ ] 资源使用没有显著增加

#### 可观测性验证
- [ ] Cilium 指标正常采集
- [ ] 链路追踪完整传递
- [ ] 日志查询正常

### 回滚策略

#### 自动回滚触发条件
- 错误率 > 1% 持续 5 分钟
- P99 延迟 > 500ms 持续 5 分钟
- 关键业务失败率显著上升

#### 回滚操作
1. 更新 HTTPRoute，权重切回 100% 旧网关
2. 观察 10 分钟
3. 如稳定，保留旧架构，排查问题

---

## 附录

### A. 参考资料
- [Cilium Gateway API 文档](https://docs.cilium.io/en/stable/network/servicemesh/gateway-api/)
- [Envoy JWT 认证](https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/filters/http/jwt_authn/v3/jwt_authn.proto)
- [Backend For Frontend 模式](https://samnewman.io/patterns/architectural/bff/)

### B. FAQ

**Q: 为什么选择 Cilium 而不是 Istio/Envoy？**
A: Cilium 在 L3/L4 网络层有优势，同时集成了 Gateway API，对于既有 Kubernetes 网络又有服务网格需求的场景更合适。

**Q: RBAC 为什么保留在 BFF 层？**
A: 电商的 RBAC 与业务逻辑强关联（如：商家只能管理自己的商品），放在 BFF 层更灵活。

**Q: 迁移期间如何保证用户体验？**
A: 采用灰度策略，每次迁移 10% 用户，并建立完善的监控和快速回滚机制。

---

**文档版本**：v1.0
**最后更新**：2026-05-28
