# Gateway 请求流程分析

> 带代码锚点的本仓网关全链路文字版；交互式架构图见根仓 `docs/architecture/ecommerce-gateway.html`。
> ⚠️ 文中 `#Lxx` 行号锚点按写作当日代码，会随提交漂移——以文件级链接 + 函数名为准，行号仅作参考。

## 一、启动流程

### 1. 配置加载

**入口**: [cmd/gateway/main.go](../cmd/gateway/main.go#L62-L73)

```go
// 先建配置源抽象（Consul / 文件由 loader 决定），再建加载器
configSource, err := loader.NewSource()                                // main.go:62
confLoader, err := config.NewSourceLoader(configSource, priorityConfigDir) // main.go:68
// 加载配置
bc, loadErr := confLoader.Load(context.Background())
```

**配置加载器**: [config/config.go](../config/config.go#L210-L254)
- 支持从 Consul KV 或本地文件加载
- YAML 转 JSON 解析
- 合并优先级目录配置

### 2. 中间件初始化

```go
// main.go:90-103
jwtErr := jwt.Init(ctx, configSource)          // JWT 初始化：加载公钥、订阅配置源热更新，失败 Fatalf
rbacErr := rbac.InitEnforcer(ctx, configSource) // RBAC 初始化：加载 Casbin 策略、订阅自动更新，失败 Fatalf
ip.Init()                                       // IP 中间件初始化（仍无参）
```

### 3. 创建代理 (Proxy)

```go
// main.go:106-112
clientFactory := client.NewFactory(makeDiscovery())   // 创建客户端工厂(含健康检查)
p, err := proxy.New(clientFactory, middleware.Create) // 创建代理
circuitbreaker.Init(clientFactory)                    // 初始化熔断器
```

### 4. 更新路由配置

**文件**: [proxy/proxy.go](../proxy/proxy.go#L401-L443)（`Proxy.Update`）

```go
// 更新配置，构建所有端点的处理器
p.Update(bc)
```

核心逻辑：
```go
func (p *Proxy) Update(c *config.Gateway) error {
    router := mux.NewRouter(...)

    for _, e := range c.Endpoints {
        // 1. 为每个端点构建处理器
        handler, closer, err := p.buildEndpoint(e, c.Middlewares)

        // 2. 注册路由
        router.Handle(e.Path, e.Method, e.Host, handler, closer)
    }

    // 3. 替换旧路由
    old := p.router.Swap(router)
}
```

---

## 二、buildEndpoint 详解 - 构建端点处理器

**文件**: [proxy/proxy.go](../proxy/proxy.go#L304-L464)

### Step 1: 创建客户端

```go
// 第 307-310 行
client, err := p.clientFactory(e)  // 根据 endpoint 配置创建客户端
```

客户端工厂 ([client/factory.go](../client/factory.go#L75-L127))：

```go
return func(endpoint *config.Endpoint) (Client, error) {
    picker := o.pickerBuilder.Build()  // 创建 P2C 选择器

    // 创建健康检查器
    healthChecker := NewHealthChecker(...)
    healthChecker.Start()

    // 创建 nodeApplier，负责监听服务变化
    applier := &nodeApplier{
        endpoint:      endpoint,
        registry:      r,           // Consul 注册中心
        picker:        picker,      // P2C 选择器
        healthChecker: healthChecker,
    }
    applier.apply(ctx)  // 启动服务监听

    // 创建客户端
    client := newClient(applier, picker, WithHealthChecker(healthChecker))

    // 如果是 gRPC 协议，创建包装器处理路径前缀
    if endpoint.Protocol == config.Protocol_GRPC && strings.HasSuffix(endpoint.Path, "*") {
        return &grpcClient{client: client, stripPrefix: stripPrefix}, nil
    }
    return client, nil
}
```

### Step 2: 构建中间件链

```go
// 第 311-324 行
tripper := http.RoundTripper(client)

// 先构建端点局部中间件
tripper, err = p.buildMiddleware(e.Middlewares, tripper)

// 再构建全局中间件
tripper, err = p.buildMiddleware(ms, tripper)
```

**中间件链构建** ([proxy/proxy.go](../proxy/proxy.go#L268-L282))：

```go
func (p *Proxy) buildMiddleware(ms []*config.Middleware, next http.RoundTripper) (http.RoundTripper, error) {
    // 先进后出，最后添加的中间件最先执行
    for i := len(ms) - 1; i >= 0; i-- {
        m, err := p.middlewareFactory(ms[i])  // 创建中间件实例
        next = m.Process(next)                   // 包装下一个处理器
    }
    return next, nil
}
```

**中间件注册** ([middleware/registry.go](../middleware/registry.go))：

```go
func Register(name string, factory Factory) {
    factories[name] = factory
}

func Create(m *config.Middleware) (MiddlewareV2, error) {
    factory := factories[m.Name]
    return factory(m)  // 调用中间件的初始化函数
}
```

---

## 三、请求处理流程

### 完整流程图

```
HTTP 请求
    ↓
Router.Match(/user/a) → 找到匹配的端点: /user*
    ↓
执行 Handler (buildEndpoint 返回的 http.HandlerFunc)
    ↓
┌─────────────────────────────────────────────────────────────────┐
│  Handler 处理流程 (第 352-463 行)                                  │
├─────────────────────────────────────────────────────────────────┤
│  1. 读取请求体                                                     │
│  2. 构建重试策略                                                   │
│  3. for i := 0; i < retryStrategy.attempts; i++ {               │
│       │                                                          │
│       ├─→ 检查熔断器 breaker.Allow()                              │
│       │     └─→ 被拒绝？→ 执行 onBreakHandler (备用服务/静态响应)    │
│       │                                                          │
│       ├─→ 执行 tripper.RoundTrip(req)                            │
│       │     ↓                                                    │
│       │   ┌──────────────────────────────────────────────────┐  │
│       │   │ 中间件链执行 (先进后出)                            │  │
│       │   │  Logging → Tracing → JWT → RBAC → BBR → Client  │  │
│       │   └──────────────────────────────────────────────────┘  │
│       │     ↓                                                    │
│       │   ┌──────────────────────────────────────────────────┐  │
│       │   │ Client.RoundTrip()                               │  │
│       │   │  ├─ selector.Select() → P2C 选择节点              │  │
│       │   │  ├─ healthCheck.HealthyNodeFilter() → 过滤不健康  │  │
│       │   │  └─ HTTP 请求发送到后端                           │  │
│       │   └──────────────────────────────────────────────────┘  │
│       │     ↓                                                    │
│       ├─→ 检查响应是否需要重试                                     │
│       │     └─→ judgeRetryRequired(conditions, resp)            │
│       │                                                          │
│       ├─→ 标记成功/失败给熔断器                                    │
│       │     breaker.MarkSuccess() / breaker.MarkFailed()       │
│       │                                                          │
│       └─→ 成功？break : 继续重试                                  │
│                                                                  │
└─────────────────────────────────────────────────────────────────┘
    ↓
返回响应给客户端
```

---

## 四、负载均衡详解

**文件**: [client/client.go](../client/client.go#L71-L149)

```go
func (c *client) RoundTrip(req *http.Request) (resp *http.Response, err error) {
    for attempt := 0; attempt < c.maxRetries; attempt++ {

        // 1. 构建节点过滤器(含健康检查)
        filters := []selector.NodeFilter{}
        if c.healthCheck != nil {
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

        // 2. P2C 选择器选择节点
        n, done, err := c.selector.Select(ctx, selector.WithNodeFilter(filters...))

        // 3. 发送请求
        reqCopy := req.Clone(ctx)
        reqCopy.URL.Host = n.Address()  // 使用选中的节点地址
        resp, err = n.(*node).client.Do(reqCopy)
    }
}
```

**P2C 算法**: [selector/p2c](../vendor/github.com/go-kratos/kratos/v2/selector/p2c/)

核心思想：随机选择两个节点，选择负载较小的那个。

---

## 五、熔断器详解

**文件**: [middleware/circuitbreaker/circuitbreaker.go](../middleware/circuitbreaker/circuitbreaker.go#L159-L181)

```go
return middleware.NewWithCloser(func(next http.RoundTripper) http.RoundTripper {
    return middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {

        // 1. 检查是否允许请求
        if err := breaker.Allow(); err != nil {
            // 被熔断，拒绝请求
            breaker.MarkFailed()      // 增加失败计数
            deniedRequestIncr(req)    // 指标 +1
            return onBreakHandler.RoundTrip(req)  // 执行熔断处理
        }

        // 2. 发送请求
        resp, err := next.RoundTrip(req)

        if err != nil {
            breaker.MarkFailed()  // 失败标记
            return nil, err
        }

        // 3. 检查响应是否成功
        if !isSuccessResponse(assertCondtions, resp) {
            breaker.MarkFailed()  // 不满足成功条件，失败标记
            return resp, nil
        }

        breaker.MarkSuccess()  // 成功标记
        return resp, nil
    })
}, closer)
```

**SRE 熔断器**: 当窗口期内请求成功率低于阈值时打开熔断器。

---

## 六、重试详解

**文件**: [proxy/proxy.go](../proxy/proxy.go#L379-L429)

```go
for i := 0; i < retryStrategy.attempts; i++ {
    if i > 0 {
        // 检查重试功能是否启用
        if !retryFeature.Enabled() {
            break
        }
        // 检查熔断器是否允许
        if err := retryBreaker.Allow(); err != nil {
            break
        }
    }

    // 设置上下文超时
    tryCtx, cancel := p.Interceptors.prepareAttemptTimeoutContext(ctx, req, retryStrategy.perTryTimeout)

    // 发送请求
    resp, err = tripper.RoundTrip(reqClone)

    if err != nil {
        // JWT 错误和权限错误不重试，直接返回
        if errors.Is(err, jwt.NotAuthN) || errors.Is(err, errorsConst.ErrPermissionDenied) {
            break
        }
        markFailed(req, i, err)
        continue  // 重试
    }

    // 检查是否需要重试 (根据响应判断)
    if !judgeRetryRequired(retryStrategy.conditions, resp) {
        // 不需要重试，请求成功
        markSuccess(req, i)
        break
    }

    markFailed(req, i, errors.New(500, "ASSERTION_FAILED", "assertion failed"))
    // continue the retry loop
}
```

**重试条件** ([proxy/condition/condition.go](../proxy/condition/condition.go))：

```go
// 支持两种条件
- byStatusCode: "502-504"  // 状态码在范围内
- byHeader:                 // 指定头部的值为指定值
  name: 'Grpc-Status'
  value: '14'               // UNAVAILABLE
```

---

## 七、限流详解

**文件**: [middleware/bbr/bbr.go](../middleware/bbr/bbr.go)

```go
func Middleware(c *config.Middleware) (middleware.Middleware, error) {
    limiter := bbr.NewLimiter()  // 创建 BBR 限流器

    return func(next http.RoundTripper) http.RoundTripper {
        return middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
            // 1. 检查是否允许通过
            done, err := limiter.Allow()
            if err != nil {
                // 被限流，返回 429
                return &http.Response{
                    StatusCode: http.StatusTooManyRequests,
                    Body:       _nopBody,
                }, nil
            }

            // 2. 执行请求
            resp, err := next.RoundTrip(req)

            // 3. 汇报结果给限流器
            done(ratelimit.DoneInfo{Err: err})
            return resp, err
        })
    }, nil
}
```

**BBR 原理**：基于探测最大带宽和最小延迟，动态调整限流阈值。

---

## 八、以请求 `/user/a` 为例的完整流程

### 配置文件

```yaml
# config.yaml
endpoints:
  - path: /user*
    protocol: GRPC
    backends:
      - target: 'discovery:///user-identity-v1'  # Consul 服务名
    timeout: 4s
    retry:
      attempts: 2
      perTryTimeout: 2s
      conditions:
        - byStatusCode: '502-504'

middlewares:
  - name: cors
  - name: logging
  - name: tracing
  - name: jwt
  - name: rbac
  - name: bbr
  - name: circuitbreaker
```

### 请求流程

```
1. 请求: POST /user/a
   ↓
2. Router 匹配: /user* → 找到端点
   ↓
3. buildEndpoint 构建处理器:
   - clientFactory(endpoint) → 创建带健康检查的客户端
   - buildMiddleware(端点中间件) → 构建中间件链
   - buildMiddleware(全局中间件) → 添加 CORS, Logging, Tracing, JWT, RBAC, BBR, CircuitBreaker
   ↓
4. Handler 执行:

   a) 检查 JWT:
      - 跳过规则匹配? → 是 → 直接到步骤 c
      - 提取 Bearer Token → 验证签名 → 解析 Claims → 设置用户信息到 Header

   b) 检查 RBAC:
      - 从 Header 获取 userID
      - 从 Casdoor 获取用户角色
      - Casbin  enforce(role, path, method) → 允许?

   c) BBR 限流:
      - limiter.Allow() → 被拒绝? → 返回 429

   d) CircuitBreaker:
      - breaker.Allow() → 被拒绝? → 返回 503 或切换备用服务

   e) 客户端请求:
      i. selector.Select() → P2C 选择一个健康节点
      ii. healthCheck.HealthyNodeFilter() → 过滤不健康节点
      iii. HTTP 请求发送到选中节点

   f) 重试逻辑:
      - 成功? → 结束
      - 失败且满足重试条件? → 回到步骤 e 重试
      - 达到最大重试次数? → 返回错误

   g) 标记结果:
      - breaker.MarkSuccess() / breaker.MarkFailed()
      - done(ratelimit.DoneInfo{Err: err})

   ↓
5. 返回响应
```

---

## 九、关键文件索引

| 功能 | 文件 |
|------|------|
| 入口 | [cmd/gateway/main.go](../cmd/gateway/main.go) |
| 代理核心 | [proxy/proxy.go](../proxy/proxy.go) |
| 客户端工厂 | [client/factory.go](../client/factory.go) |
| 客户端请求 | [client/client.go](../client/client.go) |
| 健康检查 | [client/health_checker.go](../client/health_checker.go) |
| 熔断器 | [middleware/circuitbreaker/circuitbreaker.go](../middleware/circuitbreaker/circuitbreaker.go) |
| 重试 | [proxy/retry.go](../proxy/retry.go) |
| 限流 | [middleware/bbr/bbr.go](../middleware/bbr/bbr.go) |
| 中间件注册 | [middleware/registry.go](../middleware/registry.go) |
| 路由 | [router/mux/mux.go](../router/mux/mux.go) |
| 配置加载 | [config/config.go](../config/config.go) |
