package errors

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	kratoserrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/selector"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"
)

func TestClassifyNoAvailableNode(t *testing.T) {
	// kratos 的 selector.ErrNoAvailable 是 reason 小写、message 为空的典型例子
	info := Classify(selector.ErrNoAvailable)

	if info.Status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", info.Status)
	}
	if info.Reason != ReasonNoAvailableNode {
		t.Errorf("reason = %q, want %q", info.Reason, ReasonNoAvailableNode)
	}
	if info.Message == "" {
		t.Error("message 不应为空：上游没给文案时必须兜底")
	}
}

func TestClassifySentinelErrors(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantReason string
	}{
		{"canceled", context.Canceled, StatusClientClosedRequest, ReasonCanceled},
		{"wrapped canceled", fmt.Errorf("round trip: %w", context.Canceled), StatusClientClosedRequest, ReasonCanceled},
		{"deadline", context.DeadlineExceeded, http.StatusGatewayTimeout, ReasonDeadlineExceeded},
		{"unknown", fmt.Errorf("boom"), http.StatusInternalServerError, ReasonUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := Classify(tt.err)
			if info.Status != tt.wantStatus {
				t.Errorf("status = %d, want %d", info.Status, tt.wantStatus)
			}
			if info.Reason != tt.wantReason {
				t.Errorf("reason = %q, want %q", info.Reason, tt.wantReason)
			}
		})
	}
}

func TestClassifyKeepsUpstreamMessage(t *testing.T) {
	info := Classify(kratoserrors.New(http.StatusBadRequest, "INVALID_PARAM", "key 不能为空"))
	if info.Message != "key 不能为空" {
		t.Errorf("message = %q, want 上游原文", info.Message)
	}
	if info.Reason != InvalidParam {
		t.Errorf("reason = %q, want %q", info.Reason, InvalidParam)
	}
}

func TestBuildProducesConnectCompliantDetails(t *testing.T) {
	status, body := Build(selector.ErrNoAvailable, map[string]string{"service": "config"})

	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if body.Code != CodeUnavailable {
		t.Errorf("code = %q, want %q", body.Code, CodeUnavailable)
	}
	if len(body.Details) != 1 {
		t.Fatalf("details 数量 = %d, want 1", len(body.Details))
	}

	detail := body.Details[0]
	// connect-web 的 errorFromJson 要求 type 和 value 都是非空字符串，
	// 否则会丢弃整个错误体（连 message 一起丢）
	if detail.Type != ErrorInfoType {
		t.Errorf("detail.type = %q, want %q", detail.Type, ErrorInfoType)
	}
	if detail.Value == "" {
		t.Error("detail.value 不能为空")
	}
	raw, err := base64.RawStdEncoding.DecodeString(detail.Value)
	if err != nil {
		t.Fatalf("detail.value 不是合法的 raw-std base64: %v", err)
	}
	var errorInfo errdetails.ErrorInfo
	if err := proto.Unmarshal(raw, &errorInfo); err != nil {
		t.Fatalf("detail.value 不是合法的 ErrorInfo: %v", err)
	}
	if errorInfo.GetReason() != ReasonNoAvailableNode {
		t.Errorf("ErrorInfo.reason = %q, want %q", errorInfo.GetReason(), ReasonNoAvailableNode)
	}
	if errorInfo.GetMetadata()["service"] != "config" {
		t.Errorf("ErrorInfo.metadata[service] = %q, want config", errorInfo.GetMetadata()["service"])
	}
	if detail.Debug == nil || detail.Debug.Reason != ReasonNoAvailableNode {
		t.Error("debug.reason 缺失：前端依赖它免解码取 reason")
	}
}

func TestWriteHeadersAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config.v1.ConfigService/ListKeys", nil)
	req.Header.Set("Origin", "http://localhost:3005")
	req.Header.Set("Content-Type", "application/json")

	status := Write(rec, req, selector.ErrNoAvailable, WithMetadata(map[string]string{"service": "config"}))

	if status != http.StatusServiceUnavailable || rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d / %d, want 503", status, rec.Code)
	}
	if got := rec.Header().Get(HeaderErrorReason); got != ReasonNoAvailableNode {
		t.Errorf("%s = %q, want %q", HeaderErrorReason, got, ReasonNoAvailableNode)
	}
	// Connect unary 错误规范用 application/json
	if got := rec.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:3005" {
		t.Errorf("Allow-Origin = %q", got)
	}
	// 跨域下不暴露就读不到自定义头
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != HeaderErrorReason {
		t.Errorf("Expose-Headers = %q, want %q", got, HeaderErrorReason)
	}

	var body ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v", err)
	}
	if body.Code != CodeUnavailable || body.Message == "" {
		t.Errorf("body = %+v，code 应为 unavailable 且 message 非空", body)
	}
}

func TestWriteGRPCHeaders(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)

	Write(rec, req, ErrRouteNotFound, WithGRPC(true))

	if got := rec.Header().Get("Grpc-Status"); got != "5" { // NOT_FOUND
		t.Errorf("Grpc-Status = %q, want 5", got)
	}
	// 中文文案必须百分号编码后才能进 header
	if got := rec.Header().Get("Grpc-Message"); got == "" || got == ErrRouteNotFound.Message {
		t.Errorf("Grpc-Message = %q, want 百分号编码后的文案", got)
	}
}

func TestWriteNoCORSHeadersWithoutOrigin(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/x", nil)

	Write(rec, req, ErrInternalServer)

	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("非浏览器请求不应写 CORS 头，实际 = %q", got)
	}
}

func TestConnectCodeFromHTTP(t *testing.T) {
	tests := map[int]string{
		http.StatusBadRequest:          CodeInvalidArgument,
		http.StatusUnauthorized:        CodeUnauthenticated,
		http.StatusForbidden:           CodePermissionDenied,
		http.StatusNotFound:            CodeNotFound,
		http.StatusServiceUnavailable:  CodeUnavailable,
		http.StatusGatewayTimeout:      CodeDeadlineExceeded,
		http.StatusInternalServerError: CodeInternal,
		StatusClientClosedRequest:      CodeCanceled,
		599:                            CodeInternal,
	}
	for status, want := range tests {
		if got := ConnectCodeFromHTTP(status); got != want {
			t.Errorf("ConnectCodeFromHTTP(%d) = %q, want %q", status, got, want)
		}
	}
}

func TestNormalizeReason(t *testing.T) {
	tests := []struct {
		in     string
		status int
		want   string
	}{
		{"no_available_node", 503, ReasonNoAvailableNode},
		{"NO_AVAILABLE_NODE", 503, ReasonNoAvailableNode},
		{"", 401, Unauthenticated},
		{"UNKNOWN", 500, InternalError},
		{"some.weird-reason", 500, "SOME_WEIRD_REASON"},
	}
	for _, tt := range tests {
		if got := NormalizeReason(tt.in, tt.status); got != tt.want {
			t.Errorf("NormalizeReason(%q, %d) = %q, want %q", tt.in, tt.status, got, tt.want)
		}
	}
}
