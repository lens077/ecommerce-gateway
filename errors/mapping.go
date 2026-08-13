package errors

import (
	"net/http"
	"strings"
)

// Connect 协议规范错误码（小写下划线），见 https://connectrpc.com/docs/protocol#error-codes
const (
	CodeCanceled           = "canceled"
	CodeUnknown            = "unknown"
	CodeInvalidArgument    = "invalid_argument"
	CodeDeadlineExceeded   = "deadline_exceeded"
	CodeNotFound           = "not_found"
	CodeAlreadyExists      = "already_exists"
	CodePermissionDenied   = "permission_denied"
	CodeResourceExhausted  = "resource_exhausted"
	CodeFailedPrecondition = "failed_precondition"
	CodeAborted            = "aborted"
	CodeOutOfRange         = "out_of_range"
	CodeUnimplemented      = "unimplemented"
	CodeInternal           = "internal"
	CodeUnavailable        = "unavailable"
	CodeDataLoss           = "data_loss"
	CodeUnauthenticated    = "unauthenticated"
)

// 网关自身产生的错误 reason，业务服务的 reason 由上游透传
const (
	ReasonNoAvailableNode  = "NO_AVAILABLE_NODE"
	ReasonNotFound         = "NOT_FOUND"
	ReasonMethodNotAllowed = "METHOD_NOT_ALLOWED"
	ReasonCanceled         = "CANCELED"
	ReasonDeadlineExceeded = "DEADLINE_EXCEEDED"
	ReasonBadGateway       = "BAD_GATEWAY"
	ReasonUnknown          = "UNKNOWN_ERROR"
)

// ErrorDomain 标识错误产生方，便于前端区分是网关拦下的还是上游业务返回的
const ErrorDomain = "gateway"

// httpToConnectCode 是 Connect 规范中 code -> HTTP status 映射的逆向。
// 这里返回的码会写进响应体的 code 字段，客户端以它为准（而不是靠 HTTP 状态码猜）。
var httpToConnectCode = map[int]string{
	http.StatusBadRequest:            CodeInvalidArgument,
	http.StatusUnauthorized:          CodeUnauthenticated,
	http.StatusForbidden:             CodePermissionDenied,
	http.StatusNotFound:              CodeNotFound,
	http.StatusMethodNotAllowed:      CodeUnimplemented,
	http.StatusConflict:              CodeAborted,
	http.StatusPreconditionFailed:    CodeFailedPrecondition,
	http.StatusRequestEntityTooLarge: CodeResourceExhausted,
	http.StatusUnprocessableEntity:   CodeInvalidArgument,
	http.StatusTooManyRequests:       CodeResourceExhausted,
	StatusClientClosedRequest:        CodeCanceled,
	http.StatusInternalServerError:   CodeInternal,
	http.StatusNotImplemented:        CodeUnimplemented,
	http.StatusBadGateway:            CodeUnavailable,
	http.StatusServiceUnavailable:    CodeUnavailable,
	http.StatusGatewayTimeout:        CodeDeadlineExceeded,
}

// StatusClientClosedRequest 是 nginx 约定的非标准状态码，表示客户端主动断开
const StatusClientClosedRequest = 499

// ConnectCodeFromHTTP 把 HTTP 状态码转换为 Connect 规范错误码。
func ConnectCodeFromHTTP(status int) string {
	if code, ok := httpToConnectCode[status]; ok {
		return code
	}
	switch {
	case status >= 500:
		return CodeInternal
	case status >= 400:
		return CodeInvalidArgument
	default:
		return CodeUnknown
	}
}

// NormalizeReason 把各处来源不一的 reason 统一成 UPPER_SNAKE_CASE。
// kratos 内部错误常用小写（如 selector 的 no_available_node），网关中间件用大写，
// 统一后前端只需匹配一种写法。reason 为空时按状态码给一个兜底值。
func NormalizeReason(reason string, status int) string {
	normalized := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - ('a' - 'A')
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		default:
			return '_'
		}
	}, strings.TrimSpace(reason))

	if normalized == "" || normalized == "UNKNOWN" {
		return reasonFromStatus(status)
	}
	return normalized
}

func reasonFromStatus(status int) string {
	switch status {
	case http.StatusUnauthorized:
		return Unauthenticated
	case http.StatusForbidden:
		return PermissionDenied
	case http.StatusNotFound:
		return ReasonNotFound
	case http.StatusMethodNotAllowed:
		return ReasonMethodNotAllowed
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return InvalidRequest
	case StatusClientClosedRequest:
		return ReasonCanceled
	case http.StatusGatewayTimeout:
		return ReasonDeadlineExceeded
	case http.StatusBadGateway:
		return ReasonBadGateway
	case http.StatusServiceUnavailable:
		return ServiceUnavailable
	case http.StatusInternalServerError:
		return InternalError
	default:
		return ReasonUnknown
	}
}

// reasonMessages 是 reason -> 用户可读文案的映射。
// 上游错误自带 message 时优先用上游的，这里只兜底那些 message 为空的情况
// （典型的是 kratos selector.ErrNoAvailable，reason 有值但 message 是空串）。
var reasonMessages = map[string]string{
	ReasonNoAvailableNode:  "后端服务暂不可用，请稍后重试",
	ReasonNotFound:         "请求的资源不存在",
	ReasonMethodNotAllowed: "请求方法不被允许",
	ReasonCanceled:         "请求已被取消",
	ReasonDeadlineExceeded: "请求超时，请稍后重试",
	ReasonBadGateway:       "网关无法连接到后端服务",
	ReasonUnknown:          "服务异常，请稍后重试",

	InvalidParam:       "请求参数不合法",
	MissingParam:       "缺少必要的请求参数",
	InvalidRequest:     "无效的请求参数",
	Unauthenticated:    "登录状态已失效，请重新登录",
	PermissionDenied:   "权限校验失败，请检查操作权限",
	TokenExpired:       "登录已过期，请重新登录",
	InternalError:      "服务内部错误，请稍后重试",
	ServiceUnavailable: "服务暂时不可用，请稍后再试",
	Timeout:            "请求超时，请稍后重试",
	AuthCheckFailed:    "无法验证权限",
	ForbiddenRoute:     ErrForbiddenRouteMsg,
	OperationDenied:    "操作权限不足，请联系管理员分配权限",
	InvalidFile:        "上传文件内容不能为空",
	FileReadError:      "文件读取失败",
	DataEmpty:          ErrFileContentEmptyMsg,

	// JWT 相关：网关鉴权中间件实际抛出的 reason
	"JWT_AUTHN_REQUIRED":   "未登录或登录已失效，请重新登录",
	MISSING_AUTH_TOKEN:     "缺少身份凭证，请重新登录",
	TOKEN_PARSE_FAILED:     "身份凭证无效，请重新登录",
	INVALID_TOKEN_CLAIMS:   "身份凭证无效，请重新登录",
	INVALID_SIGNING_METHOD: "身份凭证签名方式不受支持",
	PEM_DECODE_FAILED:      "服务端证书解析失败",
	CERT_PARSE_FAILED:      "服务端证书解析失败",
	NOT_RSA_PUBLIC_KEY:     "服务端证书类型错误",
}

// DefaultMessage 返回 reason 对应的用户可读文案，保证不会是空串。
func DefaultMessage(reason string, status int) string {
	if msg, ok := reasonMessages[reason]; ok && msg != "" {
		return msg
	}
	if msg, ok := reasonMessages[reasonFromStatus(status)]; ok && msg != "" {
		return msg
	}
	return reasonMessages[ReasonUnknown]
}
