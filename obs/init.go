package obs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.uber.org/zap"
)

// Shutdown flushes pending telemetry and tears down providers. Always defer it.
type Shutdown func(ctx context.Context) error

// BSPOptions tunes the BatchSpanProcessor. Zero values use defaults that mirror
// @biltme/otel: 8192 queue / 1024 batch / 2s flush.
type BSPOptions struct {
	MaxQueueSize       int
	MaxExportBatchSize int
	ScheduledDelay     time.Duration
}

// Config drives Init. ServiceName is required; everything else has sensible defaults.
type Config struct {
	ServiceName  string
	LogLevel     string
	Environment  string
	OTelEndpoint string

	Sampler             sdktrace.Sampler
	ExtraSpanProcessors []sdktrace.SpanProcessor
	ExtraResourceAttrs  []attribute.KeyValue
	BSPOptions          *BSPOptions
	HealthPaths         []string
}

const (
	defaultMaxQueueSize       = 8192
	defaultMaxExportBatchSize = 1024
	defaultScheduledDelay     = 2 * time.Second

	disableEnv = "BILT_OBS_DISABLE"
)

var (
	initMu       sync.Mutex
	initialized  bool
	cachedShut   Shutdown
	cachedLogger *zap.SugaredLogger
	cachedHealth []string
)

// Init wires global TracerProvider, MeterProvider, propagators, runtime metrics,
// and the package-level zap logger. Idempotent: a 2nd call returns the cached
// shutdown without reconfiguring. Honors BILT_OBS_DISABLE=1 (returns a no-op
// Shutdown and a stdout logger).
//
// Returns error only on config parse failures, never on endpoint reachability —
// the OTLP gRPC client reconnects asynchronously.
func Init(ctx context.Context, cfg *Config) (Shutdown, error) {
	initMu.Lock()
	defer initMu.Unlock()

	if initialized {
		return cachedShut, nil
	}

	if cfg == nil || cfg.ServiceName == "" {
		return nil, errors.New("obs.Init: Config.ServiceName is required")
	}

	if v := os.Getenv(disableEnv); v == "1" || v == "true" {
		cachedLogger = buildLogger(cfg.ServiceName, cfg.LogLevel)
		cachedHealth = resolveHealthPaths(cfg.HealthPaths)
		cachedShut = func(context.Context) error { return nil }
		initialized = true
		return cachedShut, nil
	}

	res, err := buildResource(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("obs.Init: build resource: %w", err)
	}

	tp, tpShutdown, err := buildTracerProvider(ctx, cfg, res)
	if err != nil {
		return nil, fmt.Errorf("obs.Init: tracer provider: %w", err)
	}
	otel.SetTracerProvider(tp)

	mp, mpShutdown, err := buildMeterProvider(ctx, cfg, res)
	if err != nil {
		_ = tpShutdown(ctx)
		return nil, fmt.Errorf("obs.Init: meter provider: %w", err)
	}
	otel.SetMeterProvider(mp)

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	if err := runtime.Start(runtime.WithMinimumReadMemStatsInterval(time.Second)); err != nil {
		_ = tpShutdown(ctx)
		_ = mpShutdown(ctx)
		return nil, fmt.Errorf("obs.Init: runtime metrics: %w", err)
	}

	cachedLogger = buildLogger(cfg.ServiceName, cfg.LogLevel)
	cachedHealth = resolveHealthPaths(cfg.HealthPaths)

	cachedShut = func(ctx context.Context) error {
		var errs []error
		if err := tpShutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("tracer: %w", err))
		}
		if err := mpShutdown(ctx); err != nil {
			errs = append(errs, fmt.Errorf("meter: %w", err))
		}
		if l := cachedLogger; l != nil {
			_ = l.Sync()
		}
		return errors.Join(errs...)
	}
	initialized = true
	return cachedShut, nil
}

// DefaultHealthPaths returns the canonical bilt health endpoint set. Lib skips
// these in HTTPMiddleware (no span, no log) to keep noise off SigNoz.
func DefaultHealthPaths() []string {
	return []string{"/health", "/healthz", "/health/live", "/health/ready", "/api/health"}
}

func resolveHealthPaths(in []string) []string {
	if len(in) == 0 {
		return DefaultHealthPaths()
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}

func buildResource(ctx context.Context, cfg *Config) (*resource.Resource, error) {
	attrs := []attribute.KeyValue{
		semconv.ServiceName(cfg.ServiceName),
	}
	if cfg.Environment != "" {
		attrs = append(attrs,
			semconv.DeploymentEnvironmentName(cfg.Environment),
			attribute.String("service.namespace", cfg.Environment),
		)
	}
	attrs = append(attrs, cfg.ExtraResourceAttrs...)

	return resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithProcessPID(),
		resource.WithProcessExecutableName(),
		resource.WithProcessExecutablePath(),
		resource.WithProcessCommandArgs(),
		resource.WithProcessRuntimeName(),
		resource.WithProcessRuntimeVersion(),
		resource.WithProcessRuntimeDescription(),
		resource.WithHost(),
		resource.WithAttributes(attrs...),
	)
}

func buildTracerProvider(ctx context.Context, cfg *Config, res *resource.Resource) (*sdktrace.TracerProvider, Shutdown, error) {
	exporterOpts := []otlptracegrpc.Option{otlptracegrpc.WithInsecure()}
	if cfg.OTelEndpoint != "" {
		exporterOpts = append(exporterOpts, otlptracegrpc.WithEndpointURL(cfg.OTelEndpoint))
	}
	exp, err := otlptracegrpc.New(ctx, exporterOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("trace exporter: %w", err)
	}

	bspOpts := bspOptionsOrDefault(cfg.BSPOptions)
	bsp := sdktrace.NewBatchSpanProcessor(exp,
		sdktrace.WithMaxQueueSize(bspOpts.MaxQueueSize),
		sdktrace.WithMaxExportBatchSize(bspOpts.MaxExportBatchSize),
		sdktrace.WithBatchTimeout(bspOpts.ScheduledDelay),
	)

	tpOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSpanProcessor(bsp),
	}
	for _, sp := range cfg.ExtraSpanProcessors {
		tpOpts = append(tpOpts, sdktrace.WithSpanProcessor(sp))
	}
	if cfg.Sampler != nil {
		tpOpts = append(tpOpts, sdktrace.WithSampler(cfg.Sampler))
	} else {
		tpOpts = append(tpOpts, sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())))
	}

	tp := sdktrace.NewTracerProvider(tpOpts...)
	return tp, tp.Shutdown, nil
}

func buildMeterProvider(ctx context.Context, cfg *Config, res *resource.Resource) (*sdkmetric.MeterProvider, Shutdown, error) {
	exporterOpts := []otlpmetricgrpc.Option{otlpmetricgrpc.WithInsecure()}
	if cfg.OTelEndpoint != "" {
		exporterOpts = append(exporterOpts, otlpmetricgrpc.WithEndpointURL(cfg.OTelEndpoint))
	}
	exp, err := otlpmetricgrpc.New(ctx, exporterOpts...)
	if err != nil {
		return nil, nil, fmt.Errorf("metric exporter: %w", err)
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
	)
	return mp, mp.Shutdown, nil
}

func bspOptionsOrDefault(in *BSPOptions) BSPOptions {
	out := BSPOptions{
		MaxQueueSize:       defaultMaxQueueSize,
		MaxExportBatchSize: defaultMaxExportBatchSize,
		ScheduledDelay:     defaultScheduledDelay,
	}
	if in == nil {
		return out
	}
	if in.MaxQueueSize > 0 {
		out.MaxQueueSize = in.MaxQueueSize
	}
	if in.MaxExportBatchSize > 0 {
		out.MaxExportBatchSize = in.MaxExportBatchSize
	}
	if in.ScheduledDelay > 0 {
		out.ScheduledDelay = in.ScheduledDelay
	}
	return out
}

// resetForTest clears cached state. Tests only.
func resetForTest() {
	initMu.Lock()
	defer initMu.Unlock()
	initialized = false
	cachedShut = nil
	cachedLogger = nil
	cachedHealth = nil
}
