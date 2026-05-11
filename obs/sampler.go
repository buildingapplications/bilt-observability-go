package obs

import (
	"strings"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// noisyRedisOps lists redis commands whose client spans the lib drops at the
// sampler level — before allocation, before export. These are either blocking
// polls that emit one span per loop iteration (XREADGROUP, BLPOP, SUBSCRIBE…)
// or pure heartbeats (PING) that swamp trace storage and Langfuse with no
// debugging value. Workflow Redis ops (GET/SET/XADD/etc.) are unaffected so the
// service-map agent→redis edge survives.
var noisyRedisOps = map[string]struct{}{
	"ping":       {},
	"xread":      {},
	"xreadgroup": {},
	"blpop":      {},
	"brpop":      {},
	"brpoplpush": {},
	"blmove":     {},
	"blmpop":     {},
	"bzpopmin":   {},
	"bzpopmax":   {},
	"bzmpop":     {},
	"subscribe":  {},
	"psubscribe": {},
	"ssubscribe": {},
	"wait":       {},
}

type redisNoiseSampler struct{ base sdktrace.Sampler }

func (s *redisNoiseSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	if isNoisyRedisCall(p) {
		return sdktrace.SamplingResult{
			Decision:   sdktrace.Drop,
			Tracestate: trace.SpanContextFromContext(p.ParentContext).TraceState(),
		}
	}
	return s.base.ShouldSample(p)
}

func (s *redisNoiseSampler) Description() string {
	return "redisNoiseFilter(" + s.base.Description() + ")"
}

func isNoisyRedisCall(p sdktrace.SamplingParameters) bool {
	if _, ok := noisyRedisOps[strings.ToLower(p.Name)]; !ok {
		return false
	}
	for _, kv := range p.Attributes {
		if kv.Key == "db.system" && kv.Value.AsString() == "redis" {
			return true
		}
	}
	return false
}
