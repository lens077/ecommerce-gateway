package constants

// 环境变量
const (
	// UserOwner 用户组织
	UserOwner = "OWNER"

	// ConsulAddr Consul 服务发现地址
	ConsulAddr = "CONSUL_ADDR"
	// ConsulConfigPrefix Consul 作为配置中心时的配置文件前缀
	ConsulConfigPrefix = "CONSUL_CONFIG_PREFIX"
	// ConsulConfigPath Consul 作为配置中心时的配置文件路径
	ConsulConfigPath = "CONSUL_CONFIG_PATH"
	// ConsulScheme Consul服务器的连接协议(http/https)
	ConsulScheme = "CONSUL_SCHEME"
	// ConsulInsecureSkipVerify 是否跳过 TLS 证书验证
	ConsulInsecureSkipVerify = "CONSUL_INSECURE_SKIP_VERIFY"
	// ConsulToken Consul ACL Token
	ConsulToken = "CONSUL_TOKEN"
	// ConsulDatacenter Consul 数据中心
	ConsulDatacenter = "CONSUL_DATACENTER"

	// PriorityConfigDir 优先级配置目录
	PriorityConfigDir = "PRIORITY_CONFIG"

	// JwtPubkeyPath JWT公钥路径
	JwtPubkeyPath = "JWT_PUBKEY_PATH"

	// UseTLS TLS 配置
	UseTLS    = "USE_TLS"    // 是否使用TLS
	UseHttp3  = "USE_HTTP3"  // 是否使用HTTP/3
	HTTPPort  = "HTTP_PORT"  // TCP for HTTP/1.1 & HTTP/2
	HTTP3Port = "HTTP3_PORT" // UDP for HTTP/3
	TlsDir    = "TLS_DIR"
	CrtFile   = "CRT_FILE_PATH"
	KeyFile   = "KEY_FILE_PATH"

	PoliciesfilePath = "POLICIES_FILE_PATH"
	ModelFilePath    = "MODEL_FILE_PATH"

	CasdoorUrl = "CASDOOR_URL"

	Debug = "Debug"

	// ServiceName 服务名
	ServiceName = "SERVICE_NAME"
	// ServiceAddr 服务地址
	ServiceAddr = "SERVICE_ADDR"
	// ServicePort 服务端口
	ServicePort = "SERVICE_PORT"
	// ServiceWeight 服务权重
	ServiceWeight = "SERVICE_WEIGHT"

	// ServiceTags 服务标签
	ServiceTags            = "SERVICE_TAGS"
	ProxyReadHeaderTimeout = "PROXY_READ_HEADER_TIMEOUT"
	ProxyReadTimeout       = "PROXY_READ_TIMEOUT"
	ProxyWriteTimeout      = "PROXY_WRITE_TIMEOUTT"
	ProxyIdleTimeout       = "PROXY_IDLE_TIMEOUT"
)

// 默认值
const (
	// ConfigDir 配置目录
	ConfigDir = "configs"

	// SecretsDirName 密钥目录, jwt公钥
	SecretsDirName    = "secrets"
	JwtPublicFileName = "public.pem"

	UserOwnerMetadataKey = "x-md-global-owner"
	UserNameMetadataKey  = "x-md-global-name"
	UserRoleMetadataKey  = "x-md-global-role"
	UserIdMetadataKey    = "x-md-global-user-id"

	// RBACDirName 基于角色的访问控制
	RBACDirName       = "rbac"
	PoliciesfileName  = "policies.csv"
	ModelFileFileName = "model.conf"

	TlsDirName       = "tls"
	DefaultHTTPPort  = ":443" // TCP for HTTP/1.1 & HTTP/2
	DefaultHTTP3Port = ":443" // UDP for HTTP/3
	CrtFileName      = "gateway.crt"
	KeyFileName      = "gateway.key"
)
