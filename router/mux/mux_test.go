package mux

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPathClean(t *testing.T) {
	testCases := []struct {
		Origin   string
		Expected string
	}{
		{
			"/a/b/c",
			"/a/b/c",
		},
		{
			"//a/b",
			"/a/b",
		},
		{
			"/a/b/c/",
			"/a/b/c/",
		},
		{
			"/a//b/c//",
			"/a/b/c/",
		},
	}
	for _, tc := range testCases {
		if cleanPath(tc.Origin) != tc.Expected {
			t.Errorf("cleanPath(%s) %s != %s", tc.Origin, cleanPath(tc.Origin), tc.Expected)
		}
	}
}

func TestGRPCRouteMatching(t *testing.T) {
	notFoundCalled := false
	notFoundHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		notFoundCalled = true
		w.WriteHeader(http.StatusNotFound)
	})

	router := NewRouter(notFoundHandler, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	cartHandlerCalled := false
	cartHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cartHandlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	err := router.Handle("/cart*", "POST", "", "GRPC", cartHandler, nil)
	if err != nil {
		t.Fatalf("Failed to register route: %v", err)
	}

	testCases := []struct {
		Name           string
		Path           string
		ShouldMatch    bool
	}{
		{
			Name:        "Connect-go format URL with dot",
			Path:        "/cart.v1.CartService/GetCart",
			ShouldMatch: true,
		},
		{
			Name:        "Connect-go format URL with slash",
			Path:        "/cart/v1.CartService/GetCart",
			ShouldMatch: true,
		},
		{
			Name:        "Nested method path",
			Path:        "/cart.v1.CartService/AddProductToCart",
			ShouldMatch: true,
		},
		{
			Name:        "Different service should not match",
			Path:        "/user.v1.UserService/SignIn",
			ShouldMatch: false,
		},
		{
			Name:        "Similar prefix but different service",
			Path:        "/carts.v1.CartService/GetCart",
			ShouldMatch: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			notFoundCalled = false
			cartHandlerCalled = false

			req := httptest.NewRequest(http.MethodPost, tc.Path, nil)
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			if tc.ShouldMatch {
				if notFoundCalled {
					t.Errorf("Expected route to match, but got 404")
				}
				if !cartHandlerCalled {
					t.Errorf("Expected cart handler to be called")
				}
				if w.Code != http.StatusOK {
					t.Errorf("Expected status 200, got %d", w.Code)
				}
			} else {
				if !notFoundCalled {
					t.Errorf("Expected route not to match (404), but it did match")
				}
				if cartHandlerCalled {
					t.Errorf("Expected cart handler not to be called")
				}
				if w.Code != http.StatusNotFound {
					t.Errorf("Expected status 404, got %d", w.Code)
				}
			}
		})
	}
}

func TestHTTPRouteMatching(t *testing.T) {
	notFoundCalled := false
	notFoundHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		notFoundCalled = true
		w.WriteHeader(http.StatusNotFound)
	})

	router := NewRouter(notFoundHandler, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	apiHandlerCalled := false
	apiHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiHandlerCalled = true
		w.WriteHeader(http.StatusOK)
	})

	err := router.Handle("/api/*", "GET", "", "HTTP", apiHandler, nil)
	if err != nil {
		t.Fatalf("Failed to register route: %v", err)
	}

	testCases := []struct {
		Name           string
		Path           string
		ShouldMatch    bool
	}{
		{
			Name:        "HTTP path with prefix",
			Path:        "/api/users/1",
			ShouldMatch: true,
		},
		{
			Name:        "HTTP path with trailing slash",
			Path:        "/api/",
			ShouldMatch: true,
		},
		{
			Name:        "HTTP path without prefix",
			Path:        "/other/path",
			ShouldMatch: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.Name, func(t *testing.T) {
			notFoundCalled = false
			apiHandlerCalled = false

			req := httptest.NewRequest(http.MethodGet, tc.Path, nil)
			w := httptest.NewRecorder()

			router.ServeHTTP(w, req)

			if tc.ShouldMatch {
				if notFoundCalled {
					t.Errorf("Expected route to match, but got 404")
				}
				if !apiHandlerCalled {
					t.Errorf("Expected api handler to be called")
				}
				if w.Code != http.StatusOK {
					t.Errorf("Expected status 200, got %d", w.Code)
				}
			} else {
				if !notFoundCalled {
					t.Errorf("Expected route not to match (404), but it did match")
				}
				if apiHandlerCalled {
					t.Errorf("Expected api handler not to be called")
				}
				if w.Code != http.StatusNotFound {
					t.Errorf("Expected status 404, got %d", w.Code)
				}
			}
		})
	}
}
