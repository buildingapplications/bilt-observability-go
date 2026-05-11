package obs

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type recordingBase struct {
	calls int
}

func (b *recordingBase) ShouldSample(_ sdktrace.SamplingParameters) sdktrace.SamplingResult {
	b.calls++
	return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample}
}

func (b *recordingBase) Description() string { return "recordingBase" }

func params(name string, attrs ...attribute.KeyValue) sdktrace.SamplingParameters {
	return sdktrace.SamplingParameters{
		ParentContext: context.Background(),
		Name:          name,
		Attributes:    attrs,
	}
}

func TestRedisNoiseSampler_DropsBlockingPoll(t *testing.T) {
	base := &recordingBase{}
	s := &redisNoiseSampler{base: base}

	for _, op := range []string{"xreadgroup", "XREADGROUP", "ping", "blpop", "subscribe"} {
		res := s.ShouldSample(params(op, attribute.String("db.system", "redis")))
		if res.Decision != sdktrace.Drop {
			t.Errorf("%s: expected Drop, got %v", op, res.Decision)
		}
	}
	if base.calls != 0 {
		t.Errorf("base sampler should not be invoked for noisy ops, got %d calls", base.calls)
	}
}

func TestRedisNoiseSampler_KeepsWorkflowOps(t *testing.T) {
	base := &recordingBase{}
	s := &redisNoiseSampler{base: base}

	for _, op := range []string{"get", "set", "xadd", "hget", "publish"} {
		res := s.ShouldSample(params(op, attribute.String("db.system", "redis")))
		if res.Decision != sdktrace.RecordAndSample {
			t.Errorf("%s: expected base sampler decision, got %v", op, res.Decision)
		}
	}
	if base.calls != 5 {
		t.Errorf("expected base sampler invoked 5x, got %d", base.calls)
	}
}

func TestRedisNoiseSampler_IgnoresNonRedisSpansWithMatchingName(t *testing.T) {
	base := &recordingBase{}
	s := &redisNoiseSampler{base: base}

	res := s.ShouldSample(params("ping", attribute.String("db.system", "postgres")))
	if res.Decision != sdktrace.RecordAndSample {
		t.Errorf("postgres ping should fall through to base, got %v", res.Decision)
	}

	res = s.ShouldSample(params("ping"))
	if res.Decision != sdktrace.RecordAndSample {
		t.Errorf("ping with no db.system should fall through to base, got %v", res.Decision)
	}
}

func TestRedisNoiseSampler_DescriptionWrapsBase(t *testing.T) {
	s := &redisNoiseSampler{base: &recordingBase{}}
	if got, want := s.Description(), "redisNoiseFilter(recordingBase)"; got != want {
		t.Errorf("description: got %q, want %q", got, want)
	}
}
