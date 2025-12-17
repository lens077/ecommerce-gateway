package rbac

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	fileadapter "github.com/casbin/casbin/v2/persist/file-adapter"
	config "github.com/go-kratos/gateway/api/gateway/config/v1"
	"github.com/go-kratos/gateway/constants"
	"github.com/go-kratos/gateway/middleware"
	"github.com/go-kratos/gateway/middleware/routerfilter"
	"github.com/go-kratos/gateway/pkg/loader"
	"github.com/go-kratos/kratos/v2/log"
)

var (
	logger               = log.NewHelper(log.With(log.DefaultLogger, "module", "middleware/rbac"))
	NotAuthZ             = errors.New("权限不足")
	syncedCachedEnforcer *casbin.SyncedCachedEnforcer
	enforcerMutex        sync.RWMutex
	cache                = NewCache(5*time.Minute, 10*time.Minute)
	casdoorUrl           string
	userOwner            string
	userIdMetadataKey    = constants.UserIdMetadataKey
	initialized          bool
	localPolicyFile      string
	localModelFile       string
)

// InitEnforcer 初始化RBAC系统
func InitEnforcer() error {
	if initialized {
		return nil
	}

	// 读取环境变量
	casdoorUrl = os.Getenv(constants.CasdoorUrl)
	userOwner = os.Getenv(constants.UserOwner)
	localPolicyFile = os.Getenv(constants.PoliciesfilePath)
	localModelFile = os.Getenv(constants.ModelFilePath)

	initPathsErr := initPaths()
	if initPathsErr != nil {
		return initPathsErr
	}

	load, err := loader.GetConsulLoader()
	if err != nil {
		logger.Errorf("获取Consul加载器失败: %v", err)
		return err
	}

	if err := syncEssentialFiles(load); err != nil {
		logger.Errorf("文件同步失败: %v", err)
		return err
	}

	if err := initializeEnforcer(); err != nil {
		logger.Errorf("执行器初始化失败: %v", err)
		return err
	}

	setupWatchers(load)
	middleware.Register("rbac", Middleware)
	initialized = true
	logger.Info("RBAC 系统初始化完成")

	return err
}

func initPaths() error {
	if localModelFile == "" {
		localModelFile = filepath.Join(constants.ConfigDir, constants.RBACDirName, constants.ModelFileFileName)
	}
	if localPolicyFile == "" {
		localPolicyFile = filepath.Join(constants.ConfigDir, constants.RBACDirName, constants.PoliciesfileName)
	}
	logger.Debugf("策略文件路径: %s | 模型文件路径: %s", localPolicyFile, localModelFile)

	if err := os.MkdirAll(filepath.Dir(localPolicyFile), 0o755); err != nil {
		logger.Errorf("创建策略目录失败: %v", err)
		return err
	}
	return nil
}

func syncEssentialFiles(load *loader.ConsulFileLoader) error {
	logger.Info("开始同步策略文件...")
	defer logger.Debugf("文件同步完成")

	if err := load.SyncFile(
		path.Join(constants.RBACDirName, constants.PoliciesfileName),
		localPolicyFile,
		validateFileContent,
	); err != nil {
		return err
	}

	return load.SyncFile(
		path.Join(constants.RBACDirName, constants.ModelFileFileName),
		localModelFile,
		validateFileContent,
	)
}

func validateFileContent(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("读取文件失败: %w", err)
	}

	if len(content) == 0 {
		logger.Warnf("检测到空文件: %s", path)
		return errors.New("空文件")
	}

	logger.Debugf("文件验证通过: %s (大小: %d字节)", path, len(content))
	return nil
}

func initializeEnforcer() error {
	// 记录文件哈希
	fileHash := func(path string) string {
		data, _ := os.ReadFile(path)
		return fmt.Sprintf("%x", sha256.Sum256(data))
	}

	logger.Debugf("加载模型文件: %s (SHA256: %s)",
		localModelFile,
		fileHash(localModelFile),
	)

	modelContent, err := os.ReadFile(localModelFile)
	if err != nil {
		return fmt.Errorf("读取模型文件失败: %w", err)
	}

	m, err := model.NewModelFromString(string(modelContent))
	if err != nil {
		return fmt.Errorf("创建模型失败: %w", err)
	}

	adapter := fileadapter.NewAdapter(localPolicyFile)
	enforcer, err := casbin.NewSyncedCachedEnforcer(m, adapter)
	if err != nil {
		return fmt.Errorf("创建执行器失败: %w", err)
	}

	enforcerMutex.Lock()
	defer enforcerMutex.Unlock()
	syncedCachedEnforcer = enforcer
	syncedCachedEnforcer.StartAutoLoadPolicy(1 * time.Minute)
	return nil
}

func setupWatchers(load *loader.ConsulFileLoader) {
	watchPaths := []struct {
		path     string
		callback func()
	}{
		{path.Join(constants.RBACDirName, constants.PoliciesfileName), onPolicyUpdate},
		{path.Join(constants.RBACDirName, constants.ModelFileFileName), onModelUpdate},
	}

	for _, w := range watchPaths {
		if err := load.Watch(w.path, w.callback); err != nil {
			logger.Errorf("启动监听失败: %s: %v", w.path, err)
		}
	}
}

func onPolicyUpdate() {
	logger.Info("检测到策略变更，开始处理...")
	defer logger.Info("策略更新处理完成")

	load, err := loader.GetConsulLoader()
	if err != nil {
		logger.Error(err)
		return
	}

	if err := load.SyncFile(
		path.Join(constants.RBACDirName, constants.PoliciesfileName),
		localPolicyFile,
		validateFileContent,
	); err != nil {
		logger.Errorf("策略文件同步失败: %v", err)
		return
	}

	enforcerMutex.RLock()
	defer enforcerMutex.RUnlock()
	if err := syncedCachedEnforcer.LoadPolicy(); err != nil {
		logger.Errorf("策略重载失败: %v", err)
	}
}

func onModelUpdate() {
	logger.Info("检测到模型变更，开始处理...")
	defer logger.Info("模型更新处理完成")

	// 新增文件同步逻辑
	load, err := loader.GetConsulLoader()
	if err != nil {
		logger.Errorf("获取加载器失败: %v", err)
		return
	}

	if err := load.SyncFile(
		path.Join(constants.RBACDirName, constants.ModelFileFileName),
		localModelFile,
		validateFileContent,
	); err != nil {
		logger.Errorf("模型文件同步失败: %v", err)
		return
	}

	// 重新初始化执行器
	if err := initializeEnforcer(); err != nil {
		logger.Errorf("模型重载失败: %v", err)
	}
}

type Cache struct {
	items    map[string]cacheItem
	mu       sync.RWMutex
	janitor  *cacheJanitor
	stopChan chan struct{}
}

type cacheItem struct {
	value      interface{}
	expiration int64
}

func NewCache(_, cleanupInterval time.Duration) *Cache {
	c := &Cache{
		items:    make(map[string]cacheItem),
		stopChan: make(chan struct{}),
	}

	janitor := &cacheJanitor{
		Interval: cleanupInterval,
		stop:     c.stopChan,
	}
	go janitor.Run(c)

	return c
}

func (c *Cache) Get(key string) (interface{}, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	item, exists := c.items[key]
	if !exists || time.Now().UnixNano() > item.expiration {
		return nil, false
	}
	return item.value, true
}

func (c *Cache) Set(key string, value interface{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheItem{
		value:      value,
		expiration: time.Now().Add(5 * time.Minute).UnixNano(),
	}
}

func Middleware(c *config.Middleware) (middleware.Middleware, error) {
	var routerFilter *config.Middleware_RouterFilter
	if c != nil && c.RouterFilter != nil {
		routerFilter = c.RouterFilter
	} else {
		routerFilter = &config.Middleware_RouterFilter{}
	}

	skipRules := make(map[string]map[string]bool)
	matchers := make([]*routerfilter.PathMatcher, 0)
	for _, rule := range routerFilter.Rules {
		methods := make(map[string]bool)
		for _, m := range rule.Methods {
			methods[strings.ToUpper(m)] = true
		}
		skipRules[rule.Path] = methods

		// 创建路径匹配器
		matcher, err := routerfilter.NewPathMatcher(rule.Path, rule.Methods)
		if err != nil {
			return nil, fmt.Errorf("创建路径匹配器失败: %w", err)
		}
		matchers = append(matchers, matcher)
	}
	return func(next http.RoundTripper) http.RoundTripper {
		return middleware.RoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			logger.Debugf("Processing request: %s %s", req.Method, req.URL.Path)

			// 使用PathMatcher 进行路径匹配
			skipAuth := false
			for _, matcher := range matchers {
				if ok, _ := matcher.Match(req); ok {
					logger.Infof("[RBAC] 请求匹配跳过规则，不需要权限验证: %s %s", req.Method, req.URL.Path)
					skipAuth = true
					break
				}
			}

			if skipAuth {
				return next.RoundTrip(req)
			}

			userID := req.Header.Get(userIdMetadataKey)
			if userID == "" {
				logger.Warnf("Missing user ID in request: %s", req.URL.Path)
				return nil, fmt.Errorf("%w: 缺少用户标识", NotAuthZ)
			}

			role, err := getUserRoles(userID)
			if err != nil {
				return nil, fmt.Errorf("%w: 无法验证权限", err)
			}

			enforcerMutex.RLock()
			defer enforcerMutex.RUnlock()
			allowed, syncedErr := syncedCachedEnforcer.Enforce(role, req.URL.Path, req.Method)
			if syncedErr != nil {
				return nil, fmt.Errorf("权限检查错误: %w", syncedErr)
			}

			if allowed {
				req.Header.Set(constants.UserRoleMetadataKey, role)
				req.Header.Set(constants.UserOwnerMetadataKey, userOwner)
				req.Header.Set(constants.UserIdMetadataKey, userID)
				return next.RoundTrip(req)
			}

			return nil, fmt.Errorf("%w: 角色%v无%s %s权限",
				NotAuthZ, role, req.Method, req.URL.Path)
		})
	}, nil
}

func getUserRoles(userID string) (string, error) {
	// if cached, found := cache.Get(userID); found {
	// 	return cached.(string), nil
	// }

	role, err := fetchRolesFromCasdoor(userID)
	if err != nil {
		return "", err
	}

	cache.Set(userID, role)
	return role, nil
}

func fetchRolesFromCasdoor(userID string) (string, error) {
	url := fmt.Sprintf("%s/api/get-user?userId=%s", casdoorUrl, userID)
	logger.Debugf("url2%s:", url)
	req, _ := http.NewRequest("GET", url, nil)
	q := req.URL.Query()
	// q.Add("owner", userOwner)
	req.URL.RawQuery = q.Encode()

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("casdoor接口调用失败: %w", err)
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			log.Warnf("关闭响应体失败: %v", err)
			return
		}
	}(resp.Body)

	var result struct {
		Data struct {
			Id    string     `json:"id"`
			Roles []RoleType `json:"roles"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("响应解析失败: %w", err)
	}

	var role string
	// 检查返回的用户ID是否与请求的用户ID匹配（注意：result.Data.Id是完整格式，包含owner）
	if result.Data.Id != "" && len(result.Data.Roles) > 0 {
		role = result.Data.Roles[0].Name
	}
	// 为没有角色的用户设置默认角色为普通用户
	if role == "" {
		// err := addDefaultUserRole(userOwner, "test")
		// if err != nil {
		// 	return "", fmt.Errorf("为没有角色的用户设置默认角色为普通用户: %w", err)
		// }
		role = "user"
	}
	return role, nil
}

type CasdoorRole struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	CreatedTime string `json:"createdTime"`
	DisplayName string `json:"displayName"`
	Description string `json:"description"`
}

// 对应 Casdoor 更新用户接口所需的用户信息结构体
type UpdateUserRequest struct {
	Id          string        `json:"id"`
	Name        string        `json:"name"`
	DisplayName string        `json:"displayName"`
	Roles       []CasdoorRole `json:"roles"`
}

// func addDefaultUserRole(owner, userName string) error {
// 	userId := fmt.Sprintf("%s/%s", owner, userName)
//
// 	// 定义默认角色
// 	defaultRole := CasdoorRole{
// 		Owner:       owner,                                          // 假设角色和用户在同一个 owner 下
// 		Name:        "user",                                         // 默认角色名
// 		CreatedTime: time.Now().Format("2006-01-02T15:04:05+08:00"), // 当前时间
// 		DisplayName: "用户",
// 		Description: "普通用户",
// 	}
//
// 	requestData := UpdateUserRequest{
// 		Id:          userId,
// 		Name:        userName,
// 		DisplayName: userName, // 假设 DisplayName 也是 userName
// 		Roles:       []CasdoorRole{defaultRole},
// 	}
//
// 	// 2. 序列化为 JSON
// 	jsonValue, err := json.Marshal(requestData)
// 	if err != nil {
// 		return fmt.Errorf("序列化 JSON 失败: %w", err)
// 	}
//
// 	// 3. 构建请求 URL
// 	url := fmt.Sprintf("%s/api/update-user?id=%s", casdoorUrl, userId)
// 	logger.Debugf("Casdoor Update URL: %s, Body: %s", url, string(jsonValue))
//
// 	// 4. 创建请求，将 JSON 字节数组作为请求体
// 	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(jsonValue))
//
// 	// 5. 设置 Content-Type 为 application/json
// 	req.Header.Set("Content-Type", "application/json")
//
// 	// 移除原代码中空的 Query 参数设置，因为请求体已经包含数据
// 	// q := req.URL.Query()
// 	// req.URL.RawQuery = q.Encode()
//
// 	client := &http.Client{Timeout: 3 * time.Second}
// 	resp, err := client.Do(req)
// 	if err != nil {
// 		return fmt.Errorf("Casdoor 接口调用失败: %w", err)
// 	}
// 	defer func(Body io.ReadCloser) {
// 		err := Body.Close()
// 		if err != nil {
// 			logger.Warnf("关闭响应体失败: %v", err)
// 			return
// 		}
// 	}(resp.Body)
//
// 	// 6. 检查 HTTP 状态码
// 	if resp.StatusCode != http.StatusOK {
// 		// 读取响应体以获取错误信息（可选）
// 		bodyBytes, _ := io.ReadAll(resp.Body)
// 		return fmt.Errorf("casdoor 接口返回非 200 状态码: %d, 响应: %s", resp.StatusCode, string(bodyBytes))
// 	}
//
// 	logger.Infof("成功为用户 %s/%s 添加默认角色", owner, userName)
// 	return nil
// }

type cacheJanitor struct {
	Interval time.Duration
	stop     chan struct{}
}

func (j *cacheJanitor) Run(c *Cache) {
	ticker := time.NewTicker(j.Interval)
	for {
		select {
		case <-ticker.C:
			c.cleanup()
		case <-j.stop:
			ticker.Stop()
			return
		}
	}
}

func (c *Cache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UnixNano()
	for k, v := range c.items {
		if now > v.expiration {
			delete(c.items, k)
		}
	}
}

type RoleType struct {
	Owner       string        `json:"owner"`
	Name        string        `json:"name"`
	CreatedTime time.Time     `json:"createdTime"`
	DisplayName string        `json:"displayName"`
	Description string        `json:"description"`
	Users       interface{}   `json:"users"`
	Groups      []interface{} `json:"groups"`
	Roles       []interface{} `json:"roles"`
	Domains     []interface{} `json:"domains"`
	IsEnabled   bool          `json:"isEnabled"`
}
