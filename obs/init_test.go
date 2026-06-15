package obs

import (
	"context"
	"reflect"
	"testing"

	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
)

func TestInit_RequiresServiceName(t *testing.T) {
	resetForTest()
	t.Setenv(disableEnv, "1")
	if _, err := Init(context.Background(), &Config{}); err == nil {
		t.Fatal("expected error for empty ServiceName")
	}
	if _, err := Init(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil Config")
	}
}

func TestInit_Idempotent(t *testing.T) {
	resetForTest()
	t.Setenv(disableEnv, "1")
	cfg := &Config{ServiceName: "test"}
	s1, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("first Init: %v", err)
	}
	s2, err := Init(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second Init: %v", err)
	}
	if s1 == nil || s2 == nil {
		t.Fatal("Shutdown is nil")
	}
	if reflect.ValueOf(s1).Pointer() != reflect.ValueOf(s2).Pointer() {
		t.Fatal("Init not idempotent: different shutdown returned")
	}
}

func TestInit_Killswitch(t *testing.T) {
	resetForTest()
	t.Setenv(disableEnv, "1")
	shut, err := Init(context.Background(), &Config{ServiceName: "test"})
	if err != nil {
		t.Fatalf("Init failed: %v", err)
	}
	if err := shut(context.Background()); err != nil {
		t.Fatalf("noop shutdown failed: %v", err)
	}
	if cachedLogger == nil {
		t.Fatal("logger should be initialized even with killswitch")
	}
}

func TestInit_KillswitchTrueValue(t *testing.T) {
	resetForTest()
	t.Setenv(disableEnv, "true")
	if _, err := Init(context.Background(), &Config{ServiceName: "test"}); err != nil {
		t.Fatalf("killswitch=true rejected: %v", err)
	}
}

func TestBSPOptions_Defaults(t *testing.T) {
	out := bspOptionsOrDefault(nil)
	if out.MaxQueueSize != defaultMaxQueueSize {
		t.Errorf("queue: got %d want %d", out.MaxQueueSize, defaultMaxQueueSize)
	}
	if out.MaxExportBatchSize != defaultMaxExportBatchSize {
		t.Errorf("batch: got %d want %d", out.MaxExportBatchSize, defaultMaxExportBatchSize)
	}
	if out.ScheduledDelay != defaultScheduledDelay {
		t.Errorf("delay: got %v want %v", out.ScheduledDelay, defaultScheduledDelay)
	}
}

func TestBSPOptions_Override(t *testing.T) {
	out := bspOptionsOrDefault(&BSPOptions{MaxQueueSize: 100})
	if out.MaxQueueSize != 100 {
		t.Errorf("override not applied: got %d", out.MaxQueueSize)
	}
	if out.MaxExportBatchSize != defaultMaxExportBatchSize {
		t.Errorf("default not preserved: got %d", out.MaxExportBatchSize)
	}
}

func TestDefaultHealthPaths(t *testing.T) {
	paths := DefaultHealthPaths()
	want := map[string]bool{
		"/health":       false,
		"/healthz":      false,
		"/health/live":  false,
		"/health/ready": false,
		"/api/health":   false,
	}
	for _, p := range paths {
		if _, ok := want[p]; !ok {
			t.Errorf("unexpected path %q", p)
		}
		want[p] = true
	}
	for p, seen := range want {
		if !seen {
			t.Errorf("missing path %q", p)
		}
	}
}

func TestResolveHealthPaths_CustomReplaces(t *testing.T) {
	got := resolveHealthPaths([]string{"/x"})
	if len(got) != 1 || got[0] != "/x" {
		t.Errorf("custom paths not honored: %v", got)
	}
}

func TestResolveHealthPaths_EmptyFallsBack(t *testing.T) {
	got := resolveHealthPaths(nil)
	if len(got) != len(DefaultHealthPaths()) {
		t.Errorf("expected default paths, got %v", got)
	}
}

func TestBuildResource_EnvironmentOverride(t *testing.T) {
	envKey := string(semconv.DeploymentEnvironmentName("").Key)

	t.Run("override wins over config", func(t *testing.T) {
		t.Setenv(environmentEnv, "dev-rait")
		res, err := buildResource(context.Background(), &Config{ServiceName: "svc", Environment: "production"})
		if err != nil {
			t.Fatalf("buildResource: %v", err)
		}
		if got := resourceAttr(res, envKey); got != "dev-rait" {
			t.Errorf("deployment.environment: got %q want dev-rait", got)
		}
		if got := resourceAttr(res, "service.namespace"); got != "dev-rait" {
			t.Errorf("service.namespace: got %q want dev-rait", got)
		}
	})

	t.Run("config used when override unset", func(t *testing.T) {
		res, err := buildResource(context.Background(), &Config{ServiceName: "svc", Environment: "production"})
		if err != nil {
			t.Fatalf("buildResource: %v", err)
		}
		if got := resourceAttr(res, envKey); got != "production" {
			t.Errorf("deployment.environment: got %q want production", got)
		}
	})
}

func resourceAttr(res *resource.Resource, key string) string {
	for _, kv := range res.Attributes() {
		if string(kv.Key) == key {
			return kv.Value.AsString()
		}
	}
	return ""
}
