// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mapper // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/mapper"

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/client"
)

const (
	attrArmsTraceID    = "arms.trace_id"
	attrArmsRpcID      = "arms.rpc_id"
	attrArmsRpcType    = "arms.rpc_type"
	attrArmsResultCode = "arms.result_code"
	attrArmsServiceIP  = "arms.service_ip"
	attrArmsRegionID   = "arms.region_id"
	attrArmsHaveStack  = "arms.have_stack"

	eventStackFrame = "arms.stack.frame"
)

// Options controls mapping behavior.
type Options struct {
	RegionID string
}

// ToTraces converts EagleEye spans (+ optional stacks) into OTLP traces.
// Spans for the same TraceID are grouped; different ServiceName values become
// separate ResourceSpans.
func ToTraces(spans []client.Span, stacks map[string][]client.StackFrame, opts Options) ptrace.Traces {
	td := ptrace.NewTraces()
	if len(spans) == 0 {
		return td
	}

	type key struct {
		traceID string
		service string
	}
	groups := map[key][]int{}
	order := make([]key, 0)
	for i, s := range spans {
		k := key{traceID: s.TraceID, service: s.ServiceName}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], i)
	}

	for _, k := range order {
		idxs := groups[k]
		rs := td.ResourceSpans().AppendEmpty()
		res := rs.Resource().Attributes()
		if k.service != "" {
			res.PutStr("service.name", k.service)
		}
		if opts.RegionID != "" {
			res.PutStr("cloud.region", opts.RegionID)
			res.PutStr(attrArmsRegionID, opts.RegionID)
		}

		ss := rs.ScopeSpans().AppendEmpty()
		ss.Scope().SetName("alibabacloudarmsreceiver")

		for _, i := range idxs {
			span := spans[i]
			mapSpan(ss.Spans().AppendEmpty(), span, stacks[span.RpcID], opts)
		}
	}
	return td
}

func mapSpan(out ptrace.Span, in client.Span, frames []client.StackFrame, opts Options) {
	out.SetName(in.OperationName)
	out.SetKind(spanKind(in))

	traceID, spanID := SpanIDs(in)
	_, rawTrace := parseTraceID(in.TraceID)
	out.SetTraceID(traceID)
	out.SetSpanID(spanID)
	if in.ParentSpanID != "" {
		out.SetParentSpanID(parseSpanID(in.ParentSpanID, ""))
	}

	start := msToTimestamp(in.Timestamp)
	out.SetStartTimestamp(start)
	out.SetEndTimestamp(pcommon.Timestamp(uint64(start) + uint64(in.Duration)*1_000_000))

	attrs := out.Attributes()
	if rawTrace {
		attrs.PutStr(attrArmsTraceID, in.TraceID)
	}
	if in.RpcID != "" {
		attrs.PutStr(attrArmsRpcID, in.RpcID)
	}
	if in.ResultCode != "" {
		attrs.PutStr(attrArmsResultCode, in.ResultCode)
	}
	if in.ServiceIP != "" {
		attrs.PutStr(attrArmsServiceIP, in.ServiceIP)
		attrs.PutStr("net.host.ip", in.ServiceIP)
	}
	attrs.PutBool(attrArmsHaveStack, in.HaveStack)
	if opts.RegionID != "" {
		attrs.PutStr(attrArmsRegionID, opts.RegionID)
	}

	for _, tag := range in.TagEntryList {
		if tag.Key == "" {
			continue
		}
		attrs.PutStr(tag.Key, tag.Value)
		if tag.Key == "http.status.code" {
			if n, err := strconv.ParseInt(tag.Value, 10, 64); err == nil {
				attrs.PutInt("http.response.status_code", n)
				attrs.PutInt("http.status_code", n)
			}
		}
		if tag.Key == "region" && opts.RegionID == "" {
			attrs.PutStr("cloud.region", tag.Value)
		}
	}
	attrs.PutInt(attrArmsRpcType, int64(in.RpcType))
	enrichHTTPAttributes(attrs, in)
	if _, ok := attrs.Get("db.system.name"); !ok {
		if system := dbSystemFromRpcType(in.RpcType); system != "" {
			attrs.PutStr("db.system.name", system)
		}
	}

	if in.ResultCode == "" || in.ResultCode == "0" {
		out.Status().SetCode(ptrace.StatusCodeOk)
	} else {
		out.Status().SetCode(ptrace.StatusCodeError)
		out.Status().SetMessage("arms.result_code=" + in.ResultCode)
	}

	for _, le := range in.LogEventList {
		ev := out.Events().AppendEmpty()
		ev.SetName("log")
		ev.SetTimestamp(msToTimestamp(le.Timestamp))
		for _, tag := range le.TagEntryList {
			if tag.Key != "" {
				ev.Attributes().PutStr(tag.Key, tag.Value)
			}
		}
	}

	for _, fr := range frames {
		ev := out.Events().AppendEmpty()
		ev.SetName(eventStackFrame)
		if fr.StartTime > 0 {
			ev.SetTimestamp(msToTimestamp(fr.StartTime))
		} else {
			ev.SetTimestamp(start)
		}
		ea := ev.Attributes()
		if fr.RpcID != "" {
			ea.PutStr("arms.stack.rpc_id", fr.RpcID)
		}
		if fr.Api != "" {
			ea.PutStr("code.function", fr.Api)
		}
		if fr.Line != "" {
			if n, err := strconv.ParseInt(fr.Line, 10, 64); err == nil {
				ea.PutInt("code.lineno", n)
			} else {
				ea.PutStr("code.lineno", fr.Line)
			}
		}
		if fr.Exception != "" {
			ea.PutStr("exception.message", fr.Exception)
		}
		if string(fr.ExtInfo.Type) == "20" && fr.ExtInfo.Info != "" {
			ea.PutStr("db.statement", fr.ExtInfo.Info)
		}
		if fr.Duration > 0 {
			ea.PutInt("arms.stack.duration_ms", fr.Duration)
		}
	}
}

func msToTimestamp(ms int64) pcommon.Timestamp {
	if ms <= 0 {
		return 0
	}
	return pcommon.Timestamp(uint64(ms) * 1_000_000)
}

// enrichHTTPAttributes fills OTel HTTP semantic attributes that ARMS often omits.
// Private-cloud EagleEye spans commonly only expose OperationName (e.g. "/check/health")
// and http.status.code. Downstream ClickHouse materialized views for call-chain search
// require url.path (or equivalent) and http.method / http.request.method.
func enrichHTTPAttributes(attrs pcommon.Map, in client.Span) {
	if !isHTTPSpan(in, attrs) {
		return
	}

	opMethod, opPath, opHasMethod := splitMethodAndPath(strings.TrimSpace(in.OperationName))

	method := firstAttrStr(attrs, "http.request.method", "http.method", "http.method.name")
	if method == "" && opHasMethod {
		method = opMethod
	}
	if method == "" {
		method = "GET"
	}
	putAttrIfAbsent(attrs, "http.request.method", method)
	putAttrIfAbsent(attrs, "http.method", method)

	path := firstAttrStr(attrs, "url.path", "http.route", "http.target", "http.url", "url.full")
	if path != "" {
		path = normalizeHTTPPath(path)
	} else if opPath != "" {
		path = opPath
	} else {
		path = httpPathFromOperationName(in.OperationName)
	}
	if path == "" {
		return
	}
	putAttrIfAbsent(attrs, "url.path", path)
	putAttrIfAbsent(attrs, "http.route", path)
	putAttrIfAbsent(attrs, "http.target", path)
}

func isHTTPSpan(in client.Span, attrs pcommon.Map) bool {
	switch in.RpcType {
	case 0, 25: // http server / http client
		return true
	}
	if _, ok := attrs.Get("http.status.code"); ok {
		return true
	}
	if _, ok := attrs.Get("http.response.status_code"); ok {
		return true
	}
	return httpPathFromOperationName(in.OperationName) != ""
}

func httpPathFromOperationName(op string) string {
	op = strings.TrimSpace(op)
	if op == "" {
		return ""
	}
	if method, path, ok := splitMethodAndPath(op); ok {
		_ = method
		return path
	}
	if strings.HasPrefix(op, "/") {
		return op
	}
	return ""
}

func splitMethodAndPath(op string) (method, path string, ok bool) {
	parts := strings.SplitN(op, " ", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	method = strings.ToUpper(strings.TrimSpace(parts[0]))
	path = strings.TrimSpace(parts[1])
	switch method {
	case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS", "TRACE", "CONNECT":
		if strings.HasPrefix(path, "/") {
			return method, path, true
		}
	}
	return "", "", false
}

func normalizeHTTPPath(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		if idx := strings.Index(raw, "://"); idx >= 0 {
			rest := raw[idx+3:]
			if slash := strings.Index(rest, "/"); slash >= 0 {
				raw = rest[slash:]
			} else {
				return "/"
			}
		}
	}
	if q := strings.IndexAny(raw, "?#"); q >= 0 {
		raw = raw[:q]
	}
	if raw == "" {
		return "/"
	}
	if !strings.HasPrefix(raw, "/") {
		return "/" + raw
	}
	return raw
}

func firstAttrStr(attrs pcommon.Map, keys ...string) string {
	for _, key := range keys {
		if v, ok := attrs.Get(key); ok {
			switch v.Type() {
			case pcommon.ValueTypeStr:
				if s := strings.TrimSpace(v.Str()); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func putAttrIfAbsent(attrs pcommon.Map, key, value string) {
	if value == "" {
		return
	}
	if _, ok := attrs.Get(key); ok {
		return
	}
	attrs.PutStr(key, value)
}

// SpanIDs returns the OTLP identifiers used when mapping a source span.
func SpanIDs(span client.Span) (pcommon.TraceID, pcommon.SpanID) {
	traceID, _ := parseTraceID(span.TraceID)
	return traceID, parseSpanID(span.SpanID, span.RpcID)
}

func parseTraceID(s string) (pcommon.TraceID, bool) {
	var id pcommon.TraceID
	cleaned := strings.TrimSpace(s)
	if cleaned == "" {
		return id, true
	}
	b, err := hex.DecodeString(padHex(cleaned, 32))
	if err != nil || len(b) != 16 {
		sum := sha256.Sum256([]byte(cleaned))
		copy(id[:], sum[:16])
		return id, true
	}
	copy(id[:], b)
	return id, len(cleaned) != 32
}

func parseSpanID(s, fallbackSeed string) pcommon.SpanID {
	var id pcommon.SpanID
	cleaned := strings.TrimSpace(s)
	if cleaned != "" {
		b, err := hex.DecodeString(padHex(cleaned, 16))
		if err == nil && len(b) == 8 {
			copy(id[:], b)
			return id
		}
	}
	seed := cleaned
	if seed == "" {
		seed = fallbackSeed
	}
	if seed == "" {
		return id
	}
	sum := sha256.Sum256([]byte(seed))
	copy(id[:], sum[:8])
	return id
}

func padHex(s string, width int) string {
	s = strings.TrimPrefix(strings.ToLower(s), "0x")
	if len(s) >= width {
		return s[len(s)-width:]
	}
	return strings.Repeat("0", width-len(s)) + s
}

// TraceHasError reports whether any span looks erroneous (OpenAPI semantics).
func TraceHasError(spans []client.Span) bool {
	for _, s := range spans {
		if s.ResultCode != "" && s.ResultCode != "0" {
			return true
		}
		for _, tag := range s.TagEntryList {
			if tag.Key != "http.status.code" {
				continue
			}
			if n, err := strconv.Atoi(tag.Value); err == nil && n >= 400 {
				return true
			}
		}
	}
	return false
}

// StableTraceKey returns a dedupe key for a TraceID string.
func StableTraceKey(traceID string) string {
	return strings.TrimSpace(traceID)
}

// HashUint64 is a small helper used by tests.
func HashUint64(s string) uint64 {
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint64(sum[:8])
}
