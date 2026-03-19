package errors

import (
	"net/http"

	"github.com/go-kratos/kratos/v2/errors"
)

// 业务错误常量
const (
	// 参数与请求错误（对应 HTTP 400，gRPC InvalidArgument）
	InvalidParam   = "INVALID_PARAM"
	MissingParam   = "MISSING_PARAM"
	InvalidRequest = "INVALID_REQUEST"

	// 认证与授权错误（对应 HTTP 401/403，gRPC Unauthenticated/PermissionDenied）
	Unauthenticated  = "UNAUTHENTICATED"
	PermissionDenied = "PERMISSION_DENIED"
	TokenExpired     = "TOKEN_EXPIRED"

	// 系统错误（对应 HTTP 500，gRPC Internal/Unavailable）
	InternalError      = "INTERNAL_ERROR"
	ServiceUnavailable = "SERVICE_UNAVAILABLE"
	Timeout            = "TIMEOUT"

	// AuthCheckFailed 环节权限验证过程出错（无法验证）
	AuthCheckFailed = "AUTH_CHECK_FAILED"
	// ForbiddenRoute 明确禁止访问该路由/页面
	ForbiddenRoute = "FORBIDDEN_ROUTE"
	// OperationDenied 明确禁止该操作（如无删除权限）
	OperationDenied = "OPERATION_DENIED"

	// InvalidFile 用户提交了一个没有内容的文件，属于请求参数错误
	InvalidFile = "INVALID_FILE"
	// FileReadError 文件读取过程中的系统级错误
	FileReadError = "FILE_READ_ERROR"
	DataEmpty     = "DATA_EMPTY"

	// JWT 相关错误
	PEM_DECODE_FAILED      = "PEM_DECODE_FAILED"
	CERT_PARSE_FAILED      = "CERT_PARSE_FAILED"
	NOT_RSA_PUBLIC_KEY     = "NOT_RSA_PUBLIC_KEY"
	INVALID_SIGNING_METHOD = "INVALID_SIGNING_METHOD"
	TOKEN_PARSE_FAILED     = "TOKEN_PARSE_FAILED"
	INVALID_TOKEN_CLAIMS   = "INVALID_TOKEN_CLAIMS"
	MISSING_AUTH_TOKEN     = "MISSING_AUTH_TOKEN"
)

var (
	ErrForbiddenRouteMsg   = "当前角色无权访问该资源或页面"
	ErrFileContentEmptyMsg = "读取失败：文件内容为空，请检查文件数据"
)

// 业务错误方法
var (
	// ErrPermissionDenied 权限不足/校验失败（明确知道他没有权限）
	ErrPermissionDenied = errors.New(
		http.StatusForbidden,
		PermissionDenied,
		"权限校验失败，请检查操作权限",
	)

	// ErrForbiddenRoute 针对路由/页面的访问拒绝
	// 适用场景：用户尝试进入一个其角色完全没有配置过的菜单或 URL
	ErrForbiddenRoute = errors.New(
		http.StatusForbidden,
		ForbiddenRoute,
		ErrForbiddenRouteMsg,
	)

	// ErrOperationDenied 针对具体操作的拒绝
	// 适用场景：能进页面，但点击“删除”或“导出”按钮时权限校验不通过
	ErrOperationDenied = errors.New(
		http.StatusForbidden,
		OperationDenied,
		"操作权限不足，请联系管理员分配权限",
	)

	// ErrAuthCheckFailed 由于系统逻辑导致的无法验证
	ErrAuthCheckFailed = errors.New(
		http.StatusInternalServerError,
		AuthCheckFailed,
		"无法验证权限",
	)

	ErrEmptyFile = errors.New(
		http.StatusBadRequest,
		InvalidFile,
		"上传文件内容不能为空",
	)

	// 使用 422 Unprocessable Entity 或 400
	// 422 表示请求格式正确，但语义错误（内容无法处理）
	ErrFileContentEmpty = errors.New(
		http.StatusUnprocessableEntity,
		DataEmpty,
		ErrFileContentEmptyMsg,
	)

	// ErrInvalidRequest 无效的请求
	ErrInvalidRequest = errors.New(
		http.StatusBadRequest,
		InvalidRequest,
		"无效的请求参数",
	)

	// ErrServiceUnavailable 服务不可用
	ErrServiceUnavailable = errors.New(
		http.StatusServiceUnavailable,
		ServiceUnavailable,
		"服务暂时不可用，请稍后再试",
	)
)
