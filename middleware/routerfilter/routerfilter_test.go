package routerfilter

import (
	"net/http"
	"net/http/httptest"
	"testing"

	config "github.com/go-kratos/gateway/api/gateway/config/v1"
	v1 "github.com/go-kratos/gateway/api/gateway/middleware/routerfilter/v1"
	"github.com/go-kratos/gateway/middleware"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

func TestPathMatcher(t *testing.T) {
	testCases := []struct {
		name      string
		pattern   string
		methods   []string
		reqPath   string
		reqMethod string
		expected  bool
	}{
		{"exact match", "/api/v1/users", []string{"GET"}, "/api/v1/users", "GET", true},
		{"path parameter", "/api/v1/users/{id}/profile", nil, "/api/v1/users/123/profile", "POST", true},
		{"single-level wildcard", "/v1/categories/*", nil, "/v1/categories/batch", "GET", true},
		{"multi-level wildcard", "/docs/**", nil, "/docs/chapter1/section1", "GET", true},
		{"method mismatch", "/api/delete", []string{"DELETE"}, "/api/delete", "POST", false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			matcher, err := NewPathMatcher(tc.pattern, tc.methods)
			if err != nil {
				t.Fatal(err)
			}
			matched, _ := matcher.Match(httptest.NewRequest(tc.reqMethod, tc.reqPath, nil))
			if matched != tc.expected {
				t.Fatalf("Match() = %t, want %t", matched, tc.expected)
			}
		})
	}
}

func TestMiddleware(t *testing.T) {
	mw, err := Middleware(&config.Middleware{Options: mustNewAny(&v1.RouterFilter{
		Rules: []*v1.Rule{{Path: "/api/*", Methods: []string{"GET"}}},
	})})
	if err != nil {
		t.Fatal(err)
	}
	next := middleware.RoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, nil
	})

	testCases := []struct {
		name    string
		request *http.Request
		status  int
	}{
		{"matching request", httptest.NewRequest(http.MethodGet, "/api/users", nil), http.StatusOK},
		{"rejected request", httptest.NewRequest(http.MethodPost, "/api/users", nil), http.StatusForbidden},
		{"preflight request", httptest.NewRequest(http.MethodOptions, "/api/users", nil), http.StatusNoContent},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := mw(next).RoundTrip(tc.request)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
		})
	}
}

func mustNewAny(pb proto.Message) *anypb.Any {
	a, err := anypb.New(pb)
	if err != nil {
		panic(err)
	}
	return a
}
