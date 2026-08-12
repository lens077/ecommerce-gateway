package tracing

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	config "github.com/go-kratos/gateway/api/gateway/config/v1"
	v1 "github.com/go-kratos/gateway/api/gateway/middleware/tracing/v1"
	"github.com/go-kratos/gateway/middleware"
	"github.com/go-kratos/kratos/v2"
	otelruntime "go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
)

const (
	defaultTimeout     = time.Duration(10 * time.Second)
	defaultServiceName = "gateway"
	defaultTracerName  = "gateway"
)

var globaltp = &struct {
	provider trace.TracerProvider
	initOnce sync.Once
}{}

func init() {
	middleware.Register("tracing", Middleware)
}

// Middleware is a opentelemetry middleware.
func Middleware(c *config.Middleware) (middleware.Middleware, error) {
	options := &v1.Tracing{}
	if c.Options != nil {
		if err := anypb.UnmarshalTo(c.Options, options, proto.UnmarshalOptions{Merge: true}); err != nil {
			return nil, err
		}
	}
	if globaltp.provider == nil {
		globaltp.initOnce.Do(func() {
			globaltp.provider = newTracerProvider(context.Background(), options)
			propagator := propagation.NewCompositeTextMapPropagator(propagation.Baggage{}, propagation.TraceContext{})
			otel.SetTracerProvider(globaltp.provider)
			otel.SetTextMapPropagator(propagator)

			// 网关的 OTLP endpoint 配置只存在于本中间件的 options 里,
			// 所以 MeterProvider 也在这里装配(而不是 main.go)。
			// main.go 的 otelhttp.NewHandler 已经在记 http.server.request.duration,
			// 此前因为没有 MeterProvider 一直挂在 noop 上,指标一条都没导出。
			otel.SetMeterProvider(newMeterProvider(context.Background(), options))
			// Go runtime 指标(goroutine 数、堆内存、GC 目标),与 backend 基线对齐。
			if err := otelruntime.Start(); err != nil {
				log.Printf("failed to start runtime instrumentation: %v", err)
			}
		})
	}
	tracer := otel.Tracer(defaultTracerName)
	return func(next http.RoundTripper) http.RoundTripper {
		return middleware.RoundTripperFunc(func(req *http.Request) (reply *http.Response, err error) {
			carrier := propagation.HeaderCarrier(req.Header)
			ctx := otel.GetTextMapPropagator().Extract(req.Context(), carrier)

			ctx, span := tracer.Start(
				ctx,
				fmt.Sprintf("%s %s", req.Method, req.URL.Path),
				trace.WithSpanKind(trace.SpanKindClient),
			)

			span.SetAttributes(
				semconv.HTTPRequestMethodKey.String(req.Method),
				semconv.URLPath(req.URL.Path),
				semconv.NetworkPeerAddress(req.URL.Hostname()),
			)

			otel.GetTextMapPropagator().Inject(ctx, carrier)

			defer func() {
				if err != nil {
					span.RecordError(err)
					span.SetStatus(codes.Error, err.Error())
				} else {
					span.SetStatus(codes.Ok, "OK")
				}
				if reply != nil {
					span.SetAttributes(semconv.HTTPResponseStatusCode(reply.StatusCode))
				}
				span.End()
			}()
			return next.RoundTrip(req.WithContext(ctx))
		})
	}, nil
}

func newTracerProvider(ctx context.Context, options *v1.Tracing) trace.TracerProvider {
	var (
		timeout     = defaultTimeout
		serviceName = defaultServiceName
	)

	if appInfo, ok := kratos.FromContext(ctx); ok {
		serviceName = appInfo.Name()
	}

	if options.Timeout != nil {
		timeout = options.Timeout.AsDuration()
	}

	var sampler sdktrace.Sampler
	if options.SampleRatio == nil {
		sampler = sdktrace.AlwaysSample()
	} else {
		sampler = sdktrace.TraceIDRatioBased(float64(*options.SampleRatio))
	}

	otlpoptions := []otlptracehttp.Option{
		otlptracehttp.WithEndpoint(options.HttpEndpoint),
		otlptracehttp.WithTimeout(timeout),
	}
	if options.Insecure != nil && *options.Insecure {
		otlpoptions = append(otlpoptions, otlptracehttp.WithInsecure())
	}
	if t := tlsClientConfig(options); t != nil {
		// 历史版本这里把 WithTLSClientConfig 的返回值丢掉了(没 append 进 options),
		// 且 AppendCertsFromPEM 的成功/失败分支写反 —— TLS 配置从未真正生效过。
		otlpoptions = append(otlpoptions, otlptracehttp.WithTLSClientConfig(t))
	}

	client := otlptracehttp.NewClient(
		otlpoptions...,
	)

	exporter, err := otlptrace.New(ctx, client)
	if err != nil {
		log.Fatalf("creating OTLP trace exporter: %v", err)
	}

	// attributes for all requests
	resources := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(serviceName),
	)

	return sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sampler),
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resources),
	)
}

// tlsClientConfig 把 tracing options 里的 TLS 段翻译成 *tls.Config,
// trace 与 metric 两条导出管道共用。返回 nil 表示不启用 TLS。
func tlsClientConfig(options *v1.Tracing) *tls.Config {
	if options.Tls == nil || !options.Tls.Enable {
		return nil
	}
	tlsConf := &tls.Config{InsecureSkipVerify: options.Tls.InsecureSkipVerify}
	if !tlsConf.InsecureSkipVerify && options.Tls.CaPem != "" {
		caCertPool := x509.NewCertPool()
		if ok := caCertPool.AppendCertsFromPEM([]byte(options.Tls.CaPem)); ok {
			tlsConf.RootCAs = caCertPool
		} else {
			// 解析失败退回系统根证书,可观测性配置错误不该把网关整个拉下线。
			log.Printf("failed to parse ca cert, falling back to system roots")
		}
	}
	return tlsConf
}

// newMeterProvider 用与 trace 相同的 OTLP endpoint 推送指标,30s 导出间隔与
// backend 基线一致。挂 runtime Producer 以获得 go.schedule.duration 直方图。
func newMeterProvider(ctx context.Context, options *v1.Tracing) *sdkmetric.MeterProvider {
	var (
		timeout     = defaultTimeout
		serviceName = defaultServiceName
	)
	if appInfo, ok := kratos.FromContext(ctx); ok {
		serviceName = appInfo.Name()
	}
	if options.Timeout != nil {
		timeout = options.Timeout.AsDuration()
	}

	otlpoptions := []otlpmetrichttp.Option{
		otlpmetrichttp.WithEndpoint(options.HttpEndpoint),
		otlpmetrichttp.WithTimeout(timeout),
		otlpmetrichttp.WithCompression(otlpmetrichttp.GzipCompression),
	}
	if options.Insecure != nil && *options.Insecure {
		otlpoptions = append(otlpoptions, otlpmetrichttp.WithInsecure())
	}
	if t := tlsClientConfig(options); t != nil {
		otlpoptions = append(otlpoptions, otlpmetrichttp.WithTLSClientConfig(t))
	}

	exporter, err := otlpmetrichttp.New(ctx, otlpoptions...)
	if err != nil {
		// 与 trace exporter 的处理保持一致:装配期失败直接退出,
		// 比起带着静默失效的观测跑到线上,启动时炸出来更容易被发现。
		log.Fatalf("creating OTLP metric exporter: %v", err)
	}

	resources := resource.NewWithAttributes(
		semconv.SchemaURL,
		semconv.ServiceNameKey.String(serviceName),
	)

	return sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resources),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(30*time.Second),
			sdkmetric.WithProducer(otelruntime.NewProducer()),
		)),
	)
}
