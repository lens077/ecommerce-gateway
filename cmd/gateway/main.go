package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-kratos/gateway/middleware/ip"
	"github.com/go-kratos/kratos/v2/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/go-kratos/gateway/client"
	"github.com/go-kratos/gateway/config"
	configLoader "github.com/go-kratos/gateway/config/config-loader"
	"github.com/go-kratos/gateway/constants"
	"github.com/go-kratos/gateway/discovery"
	"github.com/go-kratos/gateway/middleware"
	"github.com/go-kratos/gateway/middleware/jwt"
	"github.com/go-kratos/gateway/middleware/rbac"
	"github.com/go-kratos/gateway/pkg/loader"
	"github.com/go-kratos/gateway/proxy/auth"
	"golang.org/x/exp/rand"

	"github.com/go-kratos/gateway/proxy"
	"github.com/go-kratos/gateway/proxy/debug"
	"github.com/go-kratos/gateway/server"

	_ "net/http/pprof"

	_ "github.com/go-kratos/gateway/discovery/consul"        // Consul 服务发现
	_ "github.com/go-kratos/gateway/middleware/bbr"          // 基于 BBR 算法的流量控制
	"github.com/go-kratos/gateway/middleware/circuitbreaker" // 熔断中间件
	_ "github.com/go-kratos/gateway/middleware/cors"         // 跨域中间件
	_ "github.com/go-kratos/gateway/middleware/jwt"          // JWT 中间件
	_ "github.com/go-kratos/gateway/middleware/logging"      // 日志中间件
	_ "github.com/go-kratos/gateway/middleware/rbac"         // 基于角色的访问控制
	_ "github.com/go-kratos/gateway/middleware/rewrite"      // 重写中间件
	_ "github.com/go-kratos/gateway/middleware/routerfilter" // 路由过滤器
	_ "github.com/go-kratos/gateway/middleware/tracing"      // 链路追踪中间件
	_ "github.com/go-kratos/gateway/middleware/transcoder"   // 编解码中间件

	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/registry"
)

var (
	ctrlName          string
	ctrlService       string
	consulAddr        string
	proxyConfig       string
	priorityConfigDir string
	withDebug         bool
)

func init() {
	initConfig()
	loader.DownloadEssentialFiles()
}

func main() {
	sugar := initLogger()
	logger := sugar.Sugar()
	defer logger.Sync() // 确保日志刷盘

	// 加载主配置（会设置环境变量）
	confLoader, err := config.NewFileLoader(proxyConfig, priorityConfigDir)
	if err != nil {
		logger.Fatalf("配置加载器初始化失败: %v", err)
	}
	defer confLoader.Close()

	bc, loadErr := confLoader.Load(context.Background())
	if loadErr != nil {
		logger.Fatalf("配置加载失败: %v", loadErr)
	}

	// 4. 将配置中的envs注入环境变量
	for k, v := range bc.Envs {
		if err := os.Setenv(k, v); err != nil {
			logger.Warnf("设置环境变量失败 %s: %v", k, err)
		}
		fmt.Printf("设置环境变量 %s: %v\n", k, v)
	}

	// 5. 初始化中间件
	// JWT 中间件初始化
	jwtErr := jwt.Init()
	if jwtErr != nil {
		logger.Fatalf("JWT 中间件初始化失败: %v", jwtErr)
	}

	// RBAC 中间件初始化
	rbacErr := rbac.InitEnforcer()
	if rbacErr != nil {
		logger.Fatalf("RBAC 中间件初始化失败: %v", jwtErr)
		return
	}

	// IP 中间件初始化
	ip.Init()

	// 根据传入的服务发现创建客户端工厂
	clientFactory := client.NewFactory(makeDiscovery())
	// 创建代理 New 函数会创建基本的路由 不会根据配置端点创建路由
	p, err := proxy.New(clientFactory, middleware.Create)
	if err != nil {
		logger.Fatalf("failed to new proxy: %v", err)
	}
	circuitbreaker.Init(clientFactory)

	// 加载配置
	ctx := context.Background()
	var ctrlLoader *configLoader.CtrlConfigLoader
	if ctrlService != "" {
		logger.Infof("setup control service to: %q", ctrlService)
		ctrlLoader = configLoader.New(ctrlName, ctrlService, proxyConfig, priorityConfigDir)
		if err := ctrlLoader.Load(ctx); err != nil {
			logger.Errorf("failed to do initial load from control service: %v, using local config instead", err)
		}
		if err := ctrlLoader.LoadFeatures(ctx); err != nil {
			logger.Errorf("failed to do initial feature load from control service: %v, using default value instead", err)
		}
		go ctrlLoader.Run(ctx)
	}

	defer confLoader.Close()
	// bc, err := confLoader.Load(context.Background())
	// if err != nil {
	// 	logger.Fatalf("failed to load config: %v", err)
	// }

	// 更新服务端点配置(包括中间件) 会重置路由表 根据端点配置，创建路由处理器
	// 路由处理器中 包含一个客户端以及中间件调用链
	if err := p.Update(bc); err != nil {
		logger.Fatalf("failed to update service config bc: %v", err)
	}
	reloader := func() error {
		bc, err := confLoader.Load(context.Background())
		if err != nil {
			logger.Errorf("failed to load config: %v", err)
			return err
		}
		if err := p.Update(bc); err != nil {
			logger.Errorf("failed to update service config: %v", err)
			return err
		}
		logger.Infof("config reloaded")
		return nil
	}
	confLoader.Watch(reloader)

	var serverHandler http.Handler = p
	if withDebug {
		debug.Register("proxy", p)
		debug.Register("config", confLoader)
		if ctrlLoader != nil {
			debug.Register("ctrl", ctrlLoader)
		}
		serverHandler = debug.MashupWithDebugHandler(p)
	}

	serverHandler = auth.Handler(serverHandler)

	app := kratos.New(
		kratos.Name(bc.Name),
		kratos.Context(ctx),
		kratos.Server(
			server.NewProxy(serverHandler),
		),
	)
	if err := app.Run(); err != nil {
		logger.Errorf("failed to run servers: %v", err)
	}
}

// 从环境变量读取配置
func initConfig() {
	rand.Seed(uint64(time.Now().Nanosecond()))

	withDebug = os.Getenv(constants.Debug) == "true"
	proxyConfig = os.Getenv(constants.ConsulConfigPath)
	if proxyConfig == "" {
		proxyConfig = "config.yaml" // 默认值
	}
	priorityConfigDir = os.Getenv(constants.PriorityConfigDir)
	ctrlName = os.Getenv("CTRL_NAME")
	if ctrlName == "" {
		ctrlName = os.Getenv("advertiseName")
	}
	ctrlService = os.Getenv("ctrlService")
	consulAddr = os.Getenv(constants.ConsulAddr)
	if consulAddr == "" {
		consulAddr = "localhost:8500"
	}
	// 自动组合Consul配置路径
	if consulAddr != "" && !strings.HasPrefix(proxyConfig, "consul://") {
		// 去除consulAddr可能包含的consul://前缀
		discoveryAddr := strings.TrimPrefix(consulAddr, "consul://")
		proxyConfig = fmt.Sprintf("consul://%s/%s", discoveryAddr, proxyConfig)
	}
}

func makeDiscovery() registry.Discovery {
	if consulAddr == "" {
		return nil
	}
	d, err := discovery.Create(consulAddr)
	if err != nil {
		log.Fatalf("failed to create discovery: %v", err)
	}
	return d
}

func initLogger() *zap.Logger {
	// 1. 获取环境变量，设置默认级别为 info
	lvlStr := os.Getenv("LOG_LEVEL")
	if lvlStr == "" {
		lvlStr = "info"
	}

	// 2. 解析字符串级别 (如 "debug", "info", "error")
	var level zapcore.Level
	if err := level.UnmarshalText([]byte(lvlStr)); err != nil {
		level = zap.InfoLevel // 如果解析失败，回退到 info
	}

	// 3. 根据环境选择基础配置
	var config zap.Config
	if os.Getenv("LOG_FORMAT") == "console" {
		config = zap.NewDevelopmentConfig()
		config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		config = zap.NewProductionConfig()
		config.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder
	}

	// 4. 将解析出的级别注入配置
	config.Level = zap.NewAtomicLevelAt(level)

	logger, _ := config.Build()
	return logger
}
