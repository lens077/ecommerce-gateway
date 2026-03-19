package jwt

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/pem"
	goErrors "errors"
	"fmt"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/go-kratos/gateway/middleware/routerfilter"

	config "github.com/go-kratos/gateway/api/gateway/config/v1"
	"github.com/go-kratos/gateway/constants"
	"github.com/go-kratos/gateway/middleware"
	"github.com/go-kratos/gateway/pkg/loader"
	"github.com/go-kratos/gateway/proxy/auth"
	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/golang-jwt/jwt/v5"
)

var (
	logger        = log.NewHelper(log.With(log.DefaultLogger, "module", "middleware/jwt"))
	NotAuthN      = kratoserrors.New(401, "JWT_AUTHN_REQUIRED", "未授权: 需要身份验证")
	publicKey     *rsa.PublicKey
	publicKeyPath string
	initialized   bool
	mu            sync.RWMutex
)

func Init() error {
	if initialized {
		return nil
	}

	// 初始化公钥路径
	publicKeyPath = getPublicKeyPath()

	// 创建密钥目录
	if err := os.MkdirAll(filepath.Dir(publicKeyPath), 0o755); err != nil {
		logger.Errorf("[JWT] 创建密钥目录失败: %v", err)
		return kratoserrors.New(500, "INTERNAL_ERROR", "创建密钥目录失败")
	}

	// 获取Loader实例
	load, err := loader.GetConsulLoader()
	if err != nil {
		logger.Errorf("[JWT] 获取Loader失败: %v", err)
		return kratoserrors.New(500, "INTERNAL_ERROR", "获取Loader失败")
	}

	// 同步公钥文件
	if err := load.SyncFile(
		path.Join(constants.SecretsDirName, constants.JwtPublicFileName),
		publicKeyPath,
		validatePublicKey,
	); err != nil {
		logger.Errorf("[JWT] 公钥同步失败: %v", err)
		return kratoserrors.New(500, "INTERNAL_ERROR", "公钥同步失败")
	}

	// 初始加载公钥
	if err := reloadPublicKey(); err != nil {
		logger.Errorf("[JWT] 初始公钥加载失败: %v", err)
		return kratoserrors.New(500, "INTERNAL_ERROR", "初始公钥加载失败")
	}

	// 启动监听
	if err := load.Watch(
		path.Join(constants.SecretsDirName, constants.JwtPublicFileName),
		onPublicKeyUpdate,
	); err != nil {
		logger.Errorf("[JWT] 启动监听失败: %v", err)
		return kratoserrors.New(500, "INTERNAL_ERROR", "启动监听失败")
	}

	middleware.Register("jwt", Middleware)
	initialized = true
	logger.Info("[JWT] 初始化完成")
	return nil
}

func getPublicKeyPath() string {
	if pubPath := os.Getenv(constants.JwtPubkeyPath); pubPath != "" {
		return filepath.Clean(pubPath) // 防止路径注入
	}
	return filepath.Join(
		constants.ConfigDir,
		constants.SecretsDirName,
		constants.JwtPublicFileName,
	)
}

func onPublicKeyUpdate() {
	logger.Info("[JWT] 检测到公钥变更，开始处理...")
	defer logger.Info("[JWT] 更新处理完成")

	load, err := loader.GetConsulLoader()
	if err != nil {
		logger.Errorf("[JWT] 获取加载器失败: %v", err)
		return
	}

	// 重新同步最新公钥文件
	if err := load.SyncFile(
		path.Join(constants.SecretsDirName, constants.JwtPublicFileName),
		publicKeyPath,
		validatePublicKey,
	); err != nil {
		logger.Errorf("[JWT] 公钥同步失败: %v", err)
		return
	}

	// 重新加载公钥
	if err := reloadPublicKey(); err != nil {
		logger.Errorf("[JWT] 公钥重载失败: %v", err)
	}
}

func reloadPublicKey() error {
	mu.Lock()
	defer mu.Unlock()

	// 检查文件是否最新
	_, err := os.Stat(publicKeyPath)
	if err != nil {
		logger.Errorf("[JWT] 文件状态获取失败: %v", err)
		return kratoserrors.New(500, "INTERNAL_ERROR", "文件状态获取失败")
	}

	data, err := os.ReadFile(publicKeyPath)
	if err != nil {
		logger.Errorf("[JWT] 读取文件失败: %v", err)
		return kratoserrors.New(500, "INTERNAL_ERROR", "读取文件失败")
	}

	// 添加哈希校验
	newHash := fmt.Sprintf("%x", sha256.Sum256(data))
	if publicKey != nil {
		oldHash := fmt.Sprintf("%x", sha256.Sum256(publicKey.N.Bytes()))
		if newHash == oldHash {
			logger.Warn("[JWT] 公钥未发生实际变更")
			return nil
		}
	}
	block, _ := pem.Decode(data)
	if block == nil {
		logger.Errorf("[JWT] PEM 解码失败")
		return kratoserrors.New(400, "PEM_DECODE_FAILED", "PEM 解码失败")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		logger.Errorf("[JWT] 证书解析失败: %v", err)
		return kratoserrors.New(400, "CERT_PARSE_FAILED", "证书解析失败")
	}

	pubKey, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		logger.Errorf("[JWT] 非 RSA 公钥类型")
		return kratoserrors.New(400, "NOT_RSA_PUBLIC_KEY", "非 RSA 公钥类型")
	}

	publicKey = pubKey
	logger.Infof("[JWT] 公钥已更新 (SHA256: %s)", newHash)
	return nil
}

func validatePublicKey(tempPath string) error {
	data, err := os.ReadFile(tempPath)
	if err != nil {
		logger.Errorf("[JWT] 读取公钥文件失败: %v", err)
		return kratoserrors.New(500, "INTERNAL_ERROR", "读取公钥文件失败")
	}

	block, _ := pem.Decode(data)
	if block == nil {
		logger.Errorf("[JWT] 无效PEM格式")
		return kratoserrors.New(400, "INVALID_PEM_FORMAT", "无效PEM格式")
	}

	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		logger.Errorf("[JWT] 证书解析失败: %v", err)
		return kratoserrors.New(400, "CERT_PARSE_FAILED", "证书解析失败")
	}
	return nil
}

type CustomClaims struct {
	jwt.RegisteredClaims
	auth.User
}

func ParseJwt(tokenString string) (*CustomClaims, error) {
	mu.RLock()
	defer mu.RUnlock()

	t, err := jwt.ParseWithClaims(tokenString, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodRS256 {
			logger.Errorf("[JWT] 不支持的签名方法: %v", token.Method.Alg())
			return nil, kratoserrors.New(400, "INVALID_SIGNING_METHOD", "不支持的签名方法")
		}
		return publicKey, nil
	})

	// 首先检查是否是令牌过期错误
	if goErrors.Is(err, jwt.ErrTokenExpired) {
		logger.Warn("[JWT] 令牌已过期")
		return nil, kratoserrors.New(401, "TOKEN_EXPIRED", "令牌已过期")
	}

	// 处理其他解析错误
	if err != nil {
		logger.Errorf("[JWT] 令牌解析失败: %v", err)
		return nil, kratoserrors.New(401, "TOKEN_PARSE_FAILED", "令牌解析失败")
	}

	// 检查令牌声明是否有效
	if claims, ok := t.Claims.(*CustomClaims); ok && t.Valid {
		return claims, nil
	}

	// 处理无效的令牌声明
	logger.Errorf("[JWT] 无效的令牌声明")
	return nil, kratoserrors.New(401, "INVALID_TOKEN_CLAIMS", "无效的令牌声明")
}

func Middleware(c *config.Middleware) (middleware.Middleware, error) {
	matchers := make([]*routerfilter.PathMatcher, 0)
	if c.GetRouterFilter() != nil {
		for _, rule := range c.GetRouterFilter().Rules {
			matcher, err := routerfilter.NewPathMatcher(rule.Path, rule.Methods)
			if err != nil {
				logger.Errorf("[JWT] 创建路径匹配器失败: %v", err)
				return nil, kratoserrors.New(500, "INTERNAL_ERROR", "创建路径匹配器失败")
			}
			matchers = append(matchers, matcher)
			// 记录创建的匹配器规则
			logger.Infof("[JWT] 创建匹配器规则: %s, 方法: %v", rule.Path, rule.Methods)
		}
	}

	return func(next http.RoundTripper) http.RoundTripper {
		return middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			// 记录请求路径用于调试
			logger.Infof("[JWT] 处理请求: %s %s", req.Method, req.URL.Path)

			// 检查是否匹配跳过规则
			logger.Infof("[JWT] 开始匹配跳过规则，共有 %d 个规则", len(matchers))
			for i, matcher := range matchers {
				ok, _ := matcher.Match(req)
				logger.Infof("[JWT] 规则 %d 匹配结果: %t, 原始模式: %s, 请求路径: %s, 请求方法: %s", i, ok, matcher.RawPattern(), req.URL.Path, req.Method)
				if ok {
					logger.Infof("[JWT] 请求匹配跳过规则，不需要JWT验证: %s %s", req.Method, req.URL.Path)
					return next.RoundTrip(req)
				}
			}

			authHeader := req.Header.Get("Authorization")
			if !strings.HasPrefix(authHeader, "Bearer ") {
				logger.Warn("[JWT] 缺少Bearer token")
				return nil, kratoserrors.New(401, "MISSING_AUTH_TOKEN", "缺少Bearer token")
			}

			claims, err := ParseJwt(strings.TrimPrefix(authHeader, "Bearer "))
			if err != nil {
				logger.Errorf("[JWT] 令牌验证失败: %v", err)
				return nil, err
			}

			req.Header.Set(constants.UserIdMetadataKey, claims.User.ID)
			req.Header.Set(constants.UserNameMetadataKey, claims.User.Name)
			return next.RoundTrip(req)
		})
	}, nil
}
