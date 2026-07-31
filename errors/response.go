package errors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"strconv"
	"strings"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport/http/status"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"
)

// ErrorInfoType 是 details 里 ErrorInfo 的 proto 全名，Connect 规范要求 type 为消息全名
const ErrorInfoType = "google.rpc.ErrorInfo"

// ErrorBody 是网关所有错误响应的统一信封，严格遵循 Connect 协议：
// https://connectrpc.com/docs/protocol#error-end-stream
//
//	{"code":"unavailable","message":"...","details":[{"type":"...","value":"<base64>","debug":{...}}]}
//
// 注意 details 里每项必须同时有 string 类型的 type 和 value，
// 否则 connect-web 的 errorFromJson 会整体丢弃错误体（连 message 一起丢），
// 客户端只能退化成按 HTTP 状态码猜错误。
type ErrorBody struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Details []ErrorDetail `json:"details,omitempty"`
}

// ErrorDetail 对应 Connect 规范的 error detail。
// Debug 是规范里可选的人类可读表示，客户端无需 base64 解码即可读取。
type ErrorDetail struct {
	Type  string      `json:"type"`
	Value string      `json:"value"`
	Debug *ErrorDebug `json:"debug,omitempty"`
}

// ErrorDebug 是 ErrorInfo 的 JSON 形式，字段与 google.rpc.ErrorInfo 一致
type ErrorDebug struct {
	Reason   string            `json:"reason"`
	Domain   string            `json:"domain"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// Info 是分类之后的错误信息，Build 与 Write 共用
type Info struct {
	Status   int
	Reason   string
	Message  string
	Metadata map[string]string
}

// Classify 把任意 error 归一成状态码 + reason + 文案。
//
// 这里刻意用 errors.As 而不是 kratos 的 errors.FromError：后者对任何非 nil 的 error
// 都会返回非 nil 的 *Error（非 kratos 错误被包成 500/UNKNOWN），
// 导致 context.Canceled 这类哨兵错误的分支永远走不到。
func Classify(err error) Info {
	if err == nil {
		return Info{Status: http.StatusOK}
	}

	var kratosErr *kratoserrors.Error
	if stderrors.As(err, &kratosErr) {
		info := Info{
			Status:   int(kratosErr.Code),
			Reason:   kratosErr.Reason,
			Message:  kratosErr.Message,
			Metadata: kratosErr.Metadata,
		}
		if info.Status <= 0 {
			info.Status = http.StatusInternalServerError
		}
		return normalize(info)
	}

	info := Info{Message: err.Error()}
	switch {
	case stderrors.Is(err, context.Canceled), err.Error() == "client disconnected":
		info.Status = StatusClientClosedRequest
		info.Reason = ReasonCanceled
	case stderrors.Is(err, context.DeadlineExceeded):
		info.Status = http.StatusGatewayTimeout
		info.Reason = ReasonDeadlineExceeded
	default:
		info.Status = http.StatusInternalServerError
		info.Reason = ReasonUnknown
	}
	return normalize(info)
}

func normalize(info Info) Info {
	info.Reason = NormalizeReason(info.Reason, info.Status)
	if strings.TrimSpace(info.Message) == "" {
		info.Message = DefaultMessage(info.Reason, info.Status)
	}
	return info
}

// Build 由 error 构造出统一错误信封，同时返回应写出的 HTTP 状态码。
// extra 会并入 ErrorInfo 的 metadata（如 service / path），便于前端和日志定位。
func Build(err error, extra map[string]string) (int, ErrorBody) {
	info := Classify(err)

	metadata := make(map[string]string, len(info.Metadata)+len(extra))
	for k, v := range info.Metadata {
		metadata[k] = v
	}
	for k, v := range extra {
		if v != "" {
			metadata[k] = v
		}
	}
	if len(metadata) == 0 {
		metadata = nil
	}

	debug := &ErrorDebug{Reason: info.Reason, Domain: ErrorDomain, Metadata: metadata}
	body := ErrorBody{
		Code:    ConnectCodeFromHTTP(info.Status),
		Message: info.Message,
		Details: []ErrorDetail{{
			Type:  ErrorInfoType,
			Value: encodeErrorInfo(info.Reason, metadata),
			Debug: debug,
		}},
	}
	return info.Status, body
}

// encodeErrorInfo 按 Connect 规范把 ErrorInfo 序列化为无填充的标准 base64
// （connect-es 用的是 "std_raw"，其解码实现对是否带填充都兼容）
func encodeErrorInfo(reason string, metadata map[string]string) string {
	raw, err := proto.Marshal(&errdetails.ErrorInfo{
		Reason:   reason,
		Domain:   ErrorDomain,
		Metadata: metadata,
	})
	if err != nil {
		return ""
	}
	return base64.RawStdEncoding.EncodeToString(raw)
}

type writeOptions struct {
	metadata map[string]string
	grpc     bool
}

// WriteOption 定制错误响应的写出行为
type WriteOption func(*writeOptions)

// WithMetadata 附加到 ErrorInfo.metadata，例如 service / path
func WithMetadata(md map[string]string) WriteOption {
	return func(o *writeOptions) { o.metadata = md }
}

// WithGRPC 标记该路由是 gRPC 协议，需要额外写 Grpc-Status / Grpc-Message 头
func WithGRPC(enabled bool) WriteOption {
	return func(o *writeOptions) { o.grpc = enabled }
}

// Write 把 error 以统一格式写入响应，返回实际写出的 HTTP 状态码（供打点使用）。
// 网关内所有自行产生错误的地方都应该走这里，避免出现纯文本 / 空 body 等多种格式。
func Write(w http.ResponseWriter, r *http.Request, err error, opts ...WriteOption) int {
	o := &writeOptions{}
	for _, opt := range opts {
		opt(o)
	}

	statusCode, body := Build(err, o.metadata)

	WriteCORSHeaders(w, r)

	h := w.Header()
	h.Set(HeaderErrorReason, reasonOf(body))
	h.Set("Content-Type", errorContentType(r))
	h.Set("X-Content-Type-Options", "nosniff")

	if o.grpc {
		grpcCode := status.ToGRPCCode(statusCode)
		h.Set("Grpc-Status", strconv.Itoa(int(grpcCode)))
		h.Set("Grpc-Message", grpcPercentEncode(body.Message))
	}

	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(body)
	return statusCode
}

func reasonOf(body ErrorBody) string {
	if len(body.Details) > 0 && body.Details[0].Debug != nil {
		return body.Details[0].Debug.Reason
	}
	return ReasonUnknown
}

// errorContentType 决定错误体的 Content-Type。
// Connect 的 unary 错误规定用 application/json；只有流式请求才用 application/connect+json。
func errorContentType(r *http.Request) string {
	if r != nil && strings.HasPrefix(r.Header.Get("Content-Type"), "application/connect+") {
		return "application/connect+json; charset=utf-8"
	}
	return "application/json; charset=utf-8"
}

// grpcPercentEncode 按 gRPC 规范对 Grpc-Message 做百分号编码（头部只允许可打印 ASCII）
func grpcPercentEncode(msg string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(msg); i++ {
		c := msg[i]
		if c >= 0x20 && c <= 0x7E && c != '%' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&0x0F])
	}
	return b.String()
}
