package proxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gwerrors "github.com/go-kratos/gateway/errors"
	"github.com/go-kratos/kratos/v2/selector"
)

// decodeErrorBody 解出网关实际写到线上的错误体,用于断言前端拿到的就是 Connect 规范格式。
func decodeErrorBody(t *testing.T, rec *httptest.ResponseRecorder) gwerrors.ErrorBody {
	t.Helper()
	var body gwerrors.ErrorBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v, body=%q", err, rec.Body.String())
	}
	if body.Message == "" {
		t.Errorf("message 不能为空,前端会退化成「未知错误」")
	}
	for i, d := range body.Details {
		// connect-web 的 errorFromJson 要求 type/value 均为非空 string,否则整个错误体被丢弃
		if d.Type == "" || d.Value == "" {
			t.Errorf("details[%d] 缺少 type/value: %+v", i, d)
		}
	}
	return body
}

func TestNotFoundHandlerWritesConnectError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/__nope", nil)
	req.Header.Set("Origin", "http://localhost:3005")

	notFoundHandler(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("状态码 = %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Errorf("Content-Type = %q, want application/json(不再是 text/plain)", got)
	}
	if got := rec.Header().Get(gwerrors.HeaderErrorReason); got != gwerrors.ReasonNotFound {
		t.Errorf("%s = %q, want %q", gwerrors.HeaderErrorReason, got, gwerrors.ReasonNotFound)
	}
	if got := rec.Header().Get("Access-Control-Expose-Headers"); got != gwerrors.HeaderErrorReason {
		t.Errorf("跨域下前端读不到 %s: Expose-Headers = %q", gwerrors.HeaderErrorReason, got)
	}
	body := decodeErrorBody(t, rec)
	if body.Code != gwerrors.CodeNotFound {
		t.Errorf("code = %q, want %q", body.Code, gwerrors.CodeNotFound)
	}
	if len(body.Details) == 0 || body.Details[0].Debug == nil ||
		body.Details[0].Debug.Reason != gwerrors.ReasonNotFound {
		t.Errorf("details 里没有可直接读取的 debug.reason: %+v", body.Details)
	}
}

func TestMethodNotAllowedHandlerWritesConnectError(t *testing.T) {
	rec := httptest.NewRecorder()
	methodNotAllowedHandler(rec, httptest.NewRequest(http.MethodPatch, "/whatever", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("状态码 = %d, want 405", rec.Code)
	}
	body := decodeErrorBody(t, rec)
	if body.Code != gwerrors.CodeUnimplemented {
		t.Errorf("code = %q, want %q", body.Code, gwerrors.CodeUnimplemented)
	}
}

// 用户反馈的原始 case:上游无可用节点时,旧网关返回 code=no_available_node、message 为空。
func TestWriteErrorNoAvailableNode(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/config.v1.ConfigService/ListKeys", nil)
	req.Header.Set("Content-Type", "application/json")

	writeError(rec, req, selector.ErrNoAvailable, nil)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("状态码 = %d, want 503", rec.Code)
	}
	body := decodeErrorBody(t, rec)
	if body.Code != gwerrors.CodeUnavailable {
		t.Errorf("code = %q, want %q(不再是业务 reason)", body.Code, gwerrors.CodeUnavailable)
	}
	if body.Details[0].Debug.Reason != gwerrors.ReasonNoAvailableNode {
		t.Errorf("reason = %q, want %q", body.Details[0].Debug.Reason, gwerrors.ReasonNoAvailableNode)
	}
}
