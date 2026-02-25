# Gateway
[![Build Status](https://github.com/go-kratos/gateway/workflows/Test/badge.svg?branch=main)](https://github.com/go-kratos/gateway/actions?query=branch%3Amain)
[![codecov](https://codecov.io/gh/go-kratos/gateway/branch/main/graph/badge.svg)](https://codecov.io/gh/go-kratos/gateway)

HTTP -> Proxy -> Router -> Middleware -> Client -> Selector -> Node

## 快速入门
与后端通信的模式选择:
- discovery: 服务发现模式,使用consul作为服务注册和发现的中间件, 使用它时必须保证consul可以访问到微服务
  优点:
  - 负载均衡
  - 健康检查
  缺点:
  - 对基础设施稳定性要求高
- direct: 直连模式, 网关直接转发流量到后端
  优点:
    - 性能最好, 减少一跳
  缺点:
    - 没有负载均衡
    - 缺少健康检查

当你选择使用服务发现模式时, url的格式是: `/<path>/<service>/<method>`, path是在网关配置文件编写的`endpoints`.`path`, 例如`/product*`,那么它的url就是: `http://localhost:8080/product/product.v1.ProductService/GetProductDetail` 
当你使用直连模式时, url的格式是: `/<service>/<method>`

配置:
```yml
# 此文件用于配置网关, 项目从consul中读取服务配置, 与该配置文件进行合并
# 优先级目录合并：在优先级目录中添加或修改配置文件，观察是否生效
# 配置热更新：修改Consul中的配置或本地优先级目录的配置，确认应用能动态加载新配置
name: gateway
version: v1.4.0
# 环境变量
envs:
  # 服务发现
  DISCOVERY_DSN: consul://localhost:8500
  # 服务发现配置路径
  DISCOVERY_CONFIG_PATH: ecommerce/gateway/config.yaml
  # 输出的日志等级
  LOG_LEVEL: debug
  # 组织
  OWNER: auth
  # Casdoor 地址
  CASDOOR_URL: https://CASDOOR_URL:8081

  # 是否使用 TLS, 为 true 则使用, 需要配置CRT_FILE_PATH和KEY_FILE_PATH参数, 指定相对于入口文件(main.go)执行的路径
  USE_TLS: "false"
  USE_HTTP3: "false"
  # TCP for HTTP/1.1 & HTTP/2
  HTTP_PORT: ":8080"
  # UDP for HTTP/3
  HTTP3_PORT: ":443"
  # TLS 证书路径
  CRT_FILE_PATH: configs/tls/gateway.crt
  # TLS Key路径
  KEY_FILE_PATH: configs/tls/gateway.key

  # JWT 公钥证书
  JWT_PUBKEY_PATH: configs/secrets/public.pem

  # RBAC模型文件路径
  MODEL_FILE_PATH: configs/rbac/model.conf
  # RBAC策略文件路径
  POLICIES_FILE_PATH: configs/rbac/policies.csv

middlewares:
  - name: ip
  # 前端跨域选项
  - name: cors
    options:
      '@type': type.googleapis.com/gateway.middleware.cors.v1.Cors
      allowCredentials: true
      allowHeaders:
        - Authorization
        - Content-Type
        - X-Requested-With
        - DNT
        - Sec-Fetch-Dest
        - Sec-Fetch-Mode
        - Sec-Fetch-Site
        - Connect-Protocol-Version
        - Connect-Accept-Encoding
        - Connect-Timeout-Ms
        - Connect-Codec-Compress-Bin
      allowOrigins:
        - http://localhost:3000
        - localhost:3000
      allowMethods:
        - OPTIONS
        - GET
        - POST
        - PUT
        - PATCH
        - DELETE
  - name: logging
  - name: tracing
    options:
      '@type': type.googleapis.com/gateway.middleware.tracing.v1.Tracing
      httpEndpoint: 192.168.3.108:4318
      insecure: true
  #     认证
  - name: jwt
    # 无需认证的接口
    router_filter:
      rules:
        - path: /search/search.v1.SearchService/Search
          methods:
            - POST
            - OPTIONS
        - path: /user/user.v1.UserService/SignIn
          methods:
            - POST
            - OPTIONS
        - path: /product/product.v1.ProductService/GetProductDetail
          methods:
            - POST
            - OPTIONS

    # 基于用户的接口权限控制
  - name: rbac
    # 不需要鉴权的接口
    router_filter:
      rules:
        - path: /search/search.v1.SearchService/Search
          methods:
            - POST
            - OPTIONS
        - path: /user/user.v1.UserService/SignIn
          methods:
            - POST
            - OPTIONS
        - path: /product/product.v1.ProductService/GetProductDetail
          methods:
            - POST
            - OPTIONS

endpoints:
  - path: /user*
    protocol: GRPC
    #    middlewares:
    #      - name: rewrite
    #        options:
    #          '@type': type.googleapis.com/gateway.middleware.rewrite.v1.Rewrite
    #          stripPrefix: /user
    backends:
      #- target: 'direct://localhost:30001' # 直连模式
      - target: 'discovery:///user-identity-v1' # 服务发现模式
    timeout: 4s
    retry:
      attempts: 2
      perTryTimeout: 2s
      conditions:
        - byStatusCode: '502-504'
        - byHeader:
            name: 'Grpc-Status'
            value: '14'

  - path: /search*
    protocol: GRPC
    #          stripPrefix: /user
    backends:
      #- target: 'direct://localhost:30002'
      - target: 'discovery:///search-product-v1'
    timeout: 4s
    retry:
      attempts: 2
      perTryTimeout: 2s
      conditions:
        - byStatusCode: '502-504'
        - byHeader:
            name: 'Grpc-Status'
            value: '14'

  - path: /product*
    protocol: GRPC
    backends:
      - target: 'direct://localhost:30003'
      #- target: 'discovery:///product-core-v1'
    timeout: 4s
    retry:
      attempts: 2
      perTryTimeout: 2s
      conditions:
        - byStatusCode: '502-504'
        - byHeader:
            name: 'Grpc-Status'
            value: '14'

```


## TLS
1. 开发测试时可以使用自签名证书, 生产需要使用真实的证书, 项目支持自签名证书的生成, 可以使用以下命令生成自签名证书:
```bash
make https
```

2. 创建TLS配置

- USE_TLS: bool, 告诉网关使用TLS , 默认使用h2c协议, 即HTTP/2 over TCP, 即HTTP/2的明文传输
- USE_HTTP3: bool, 告诉网关使用HTTP/3 + QUIC, 实现使用 quic-go/quic-go
- HTTP_PORT: string, 告诉网关使用的端口, 使用TCP for HTTP/1.1 & HTTP/2, 例如: ":443"
- HTTP3_PORT: string, 告诉网关使用的端口, 使用UDP for HTTP/3, 例如: ":443", 当 TCP 和 UDP 端口重合时, 网关通过TLS的ALPN自动协商实现无缝回退
- CRT_FILE_PATH: string, 告诉网关使用的证书文件路径, 例如: "dynamic-config/tls/gateway.crt", 开发时保持默认值即可, 生产环境时需要把证书替换并修改名为`gateway.crt`
- KEY_FILE_PATH: string, 告诉网关使用的证书文件路径, 例如: "dynamic-config/tls/gateway.key", 开发时保持默认值即可, 生产环境时需要把证书替换并修改名为`gateway.key`
最低可运行示例:
修改`cmd/gateway/config.yaml` 然后复制到 Consul KV 中
```yaml
envs:
  # 服务发现
  DISCOVERY_DSN: consul://example.com:8500
  # 服务发现配置路径
  DISCOVERY_CONFIG_PATH: ecommerce/gateway/config.yaml
  # 是否使用 TLS, 为 true 则使用, 需要配置CRT_FILE_PATH和KEY_FILE_PATH参数, 指定相对于入口文件(main.go)执行的路径
  USE_TLS: "true"
  USE_HTTP3: "true"
  # TCP for HTTP/1.1 & HTTP/2
  HTTP_PORT: ":443"
  # UDP for HTTP/3
  HTTP3_PORT: ":443"
  # TLS 证书路径
  CRT_FILE_PATH: dynamic-config/tls/gateway.crt
  # TLS Key路径
  KEY_FILE_PATH: dynamic-config/tls/gateway.key
```

![img.png](img.png)

# Middleware
* cors
* auth
* color
* logging
* tracing
* metrics
* ratelimit
* datacenter
* jwt: 与casdoor集成
* rbac: 与casdoor的集成, 使用到了redis来缓存casbin策略, 基于角色的接口的权限控制
* router_filter: 路由过滤器, 用于过滤掉不需要的路由

### CORS

前端一般都要包含如下请求头:
```yaml
allowHeaders:
  - Authorization
  - Content-Type
  - X-Requested-With
  - DNT
  - Sec-Fetch-Dest
  - Sec-Fetch-Mode
  - Sec-Fetch-Site

```
站点规则如下:
请求来源	配置项	是否允许
- http://a.localhost:3000	.localhost	✅
- http://localhost:8080	.localhost	✅
- http://x.y.localhost	*.localhost	✅
- http://evil.localhost.com	.localhost	❌
- http://127.0.0.1:3000	127.0.0.1:3000	✅
如果需要修改, 可以修改`middleware/cors/cors.go`中的代码的`isOriginAllowed` 函数

### RouterFilter
路由过滤器, 用于过滤掉不需要的路由, 目前只支持正则匹配, 不支持通配符匹配, 不支持前缀匹配, 不支持后缀匹配, 不支持路径参数匹配,
该 router_filter 中间件支持以下类型的路由规则：

1. 精确路径匹配
   规则示例 ：/v1/products
   匹配行为 ：仅匹配完全相同的路径
   代码依据 ：正则表达式直接编译路径为精确匹配模式 
2. 通配符匹配
   a. 单层通配符 (/*)
   规则示例 ：/v1/products/*
   匹配行为 ：匹配单级子路径（如 /v1/products/123）
   实现原理 ：正则表达式将 /* 转换为 [^/]+ 
   b. 多层通配符 (/**)
   规则示例 ：/v1/products/**
   匹配行为 ：匹配多级子路径（如 /v1/products/123/details）
   实现原理 ：正则表达式将 /** 转换为 .+ 
3. 路径参数捕获
   规则示例 ：/v1/products/{id}
   匹配行为 ：提取路径参数（如 id=123）
   实现原理 ：通过正则表达式命名捕获组 (?P<id>[^/]+) 
4. HTTP 方法限制
   规则示例 ：
    ```yaml
    - path: /v1/products
      methods: [GET, POST]
    ```
- path: /v1/products
  methods: [GET, POST]
  匹配行为 ：仅允许指定的 HTTP 方法
  实现原理 ：检查请求方法是否在允许列表中 
5. 混合规则（路径 + 方法）
   规则示例 ：
```yaml
- path: /v1/auth
  methods: [POST, OPTIONS]
```

- path: /v1/auth
  methods: [POST, OPTIONS]
  匹配行为 ：同时满足路径和方法条件的请求才会被放行
  实现原理 ：路径和方法检查在 PathMatcher.Match() 中联合执行 
6. CORS 预检请求自动放行
   规则示例 ：所有 OPTIONS 请求
   匹配行为 ：直接返回 CORS 响应头，跳过后续中间件
   实现原理 ：在中间件入口处特殊处理 OPTIONS 方法 

配置示例:
```yaml
middlewares:
  - name: router_filter
    options:
      "@type": type.googleapis.com/gateway.middleware.routerfilter.v1.RouterFilter
      rules:
        # 精确路径 + 方法限制
        - path: /v1/auth
          methods: [POST, OPTIONS]
        
        # 通配符匹配
        - path: /v1/products/**
          methods: [GET]
        
        # 路径参数捕获
        - path: /v1/orders/{order_id}
          methods: [GET, DELETE]
```

## JWT
证书使用`x509`生成,4096位大小,加密算法是RS256(RSA+SHA256),有效期20年. 
证书文件在`/cmd/gatway`目录下, 证书文件名为`public.pem`

## RBAC

目前使用了官方的casbin的redis插件来缓存策略, 不一定是Redis, 也可以是任何支持redis协议的`rpush`工具即可 
目前的redis实例是没有设置密码的, 如果需要设置密码, 可以修改`middleware/rbac/rbac.go`中的代码的`initEnforcer` 函数,
常用的函数如下:
- 无加密: redisadapter.NewAdapter
- 包含密码: func NewAdapterWithPassword(network string, address string, password string) (*Adapter, error)
- 包含用户和密码: func NewAdapterWithUser(network string, address string, username string, password string) (*Adapter, error)

```go
package rbac

import (
	"fmt"
	"github.com/casbin/casbin/v2"
	redisadapter "github.com/casbin/redis-adapter/v3"
)

func initEnforcer() {
	a, err := redisadapter.NewAdapter("tcp", RedisAddr)
	if err != nil {
		panic(fmt.Errorf("failed to initialize redis adapter: %v", err))
	}

	enforcer, err := casbin.NewSyncedCachedEnforcer("./rbac_model.conf", a)
	if err != nil {
		panic(fmt.Errorf("failed to initialize enforcer: %v", err))
	}
	syncedCachedEnforcer = enforcer

	// 初始化策略
	initPolicies(enforcer)
}

```

当前模型:
```
[request_definition]
r = sub, obj, act

[policy_definition]
p = sub, obj, act, eft

[role_definition]
g = _, _
g2 = _, _

[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))

[matchers]
m = g(r.sub, p.sub) && keyMatch2(r.obj, p.obj) && regexMatch(r.act, p.act)
```

策略:
```csv
p, public, /v1/auth, POST, allow
p, public, /v1/products, GET, allow
p, public, /ecommerce.product.v1.ProductService/*, POST, allow

p, user, /v1/auth/profile, GET, allow
p, user, /v1/users/*, (GET|POST|PATCH|DELETE), allow
p, user, /v1/carts, (GET|POST|PATCH|DELETE), allow
p, user, /v1/carts/*, (GET|POST|DELETE), allow
p, user, /v1/checkout/*, POST, allow
p, user, /v1/orders, (GET|POST), allow
p, user, /v1/categories/*, GET, allow

p, merchant, /v1/products*, (GET|POST|PUT|DELETE), allow
p, merchant, /v1/products/*/submit-audit, POST, allow
p, merchant, /v1/categories/*, POST, allow
p, merchant, /v1/merchants, (GET|POST|PUT|DELETE|PATCH), allow

p, admin, /v1/users/*, (POST|PUT|DELETE|PATCH), allow
p, admin, /v1/categories/*, (POST|PUT|DELETE|PATCH), allow
p, admin, /v1/products/*, (GET|POST|PUT|DELETE|PATCH), allow
p, admin, /v1/products/*/audit, POST, allow
p, admin, /v1/merchants/*, (GET|POST|PUT|DELETE|PATCH), allow
p, admin, /v1/orders/*/paid, POST, allow
p, admin, /ecommerce.product.v1.ProductService/*, (POST|PUT|DELETE|PATCH), allow
p, anyone, /*, .*, deny

g, user, public
g, merchant, user
g, admin, merchant
```
