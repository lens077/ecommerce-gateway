package errors

import "net/http"

// 网关自身产生的错误（鉴权失败、无可用节点、404 等）不会经过 middleware/cors，
// 浏览器拿不到跨域响应就只会看到一个 CORS 报错而读不到真正的错误体，
// 所以这些路径必须自己补上跨域响应头。
const (
	corsAllowMethods = "GET, POST, PUT, DELETE, OPTIONS"
	corsAllowHeaders = "Origin, Content-Length, Content-Type, Authorization, Connect-Protocol-Version, Connect-Accept-Encoding, Connect-Timeout-Ms, Connect-Codec-Compress-Bin"
	corsMaxAge       = "600"

	// HeaderErrorReason 让 curl / 非 Connect 客户端不用解析 body 就能拿到业务 reason
	HeaderErrorReason = "X-Error-Reason"
)

// corsExposeHeaders 必须显式声明，否则跨域场景下浏览器读不到自定义响应头
const corsExposeHeaders = HeaderErrorReason

// WriteCORSHeaders 为错误响应补齐跨域头。无 Origin（非浏览器请求）时不做任何事。
func WriteCORSHeaders(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Methods", corsAllowMethods)
	h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
	h.Set("Access-Control-Max-Age", corsMaxAge)
}
