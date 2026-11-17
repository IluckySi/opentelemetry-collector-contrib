// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mapper

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/client"
)

func TestToTracesErrorFixture(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "get_trace.json"))
	require.NoError(t, err)
	var resp client.GetTraceResponse
	require.NoError(t, json.Unmarshal(raw, &resp))

	stackRaw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "get_stack.json"))
	require.NoError(t, err)
	var stack client.GetStackResponse
	require.NoError(t, json.Unmarshal(stackRaw, &stack))

	td := ToTraces(resp.Spans, map[string][]client.StackFrame{"0": stack.StackInfo}, Options{RegionID: "test-region"})
	require.Equal(t, 2, td.ResourceSpans().Len())

	var root ptrace.Span
	foundRoot := false
	for i := 0; i < td.ResourceSpans().Len(); i++ {
		rs := td.ResourceSpans().At(i)
		svc, _ := rs.Resource().Attributes().Get("service.name")
		spans := rs.ScopeSpans().At(0).Spans()
		require.Equal(t, 1, spans.Len())
		sp := spans.At(0)
		require.Equal(t, ptrace.StatusCodeError, sp.Status().Code())
		rpc, ok := sp.Attributes().Get("arms.rpc_id")
		require.True(t, ok)
		if rpc.Str() == "0" {
			require.Equal(t, "test-frontend", svc.Str())
			require.True(t, isEmptySpanID(sp.ParentSpanID()))
			require.Equal(t, 3, sp.Events().Len())
			require.Equal(t, "arms.stack.frame", sp.Events().At(0).Name())
			function, ok := sp.Events().At(0).Attributes().Get("code.function")
			require.True(t, ok)
			require.Equal(t, "Tomcat Servlet Process", function.Str())
			exception, ok := sp.Events().At(1).Attributes().Get("exception.message")
			require.True(t, ok)
			require.NotEmpty(t, exception.Str())
			statement, ok := sp.Events().At(2).Attributes().Get("db.statement")
			require.True(t, ok)
			require.Equal(t, "SELECT count(0) FROM demo_table", statement.Str())
			root = sp
			foundRoot = true
		}
	}
	require.True(t, foundRoot)
	require.Equal(t, "/api/example", root.Name())
	require.True(t, TraceHasError(resp.Spans))
}

func TestHTTPStatusCodeAttributeIsInteger(t *testing.T) {
	for _, tc := range []struct {
		name        string
		value       string
		wantPresent bool
		wantValue   int64
	}{
		{name: "numeric", value: "500", wantPresent: true, wantValue: 500},
		{name: "non-numeric", value: "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			td := ToTraces([]client.Span{{
				TraceID: "0123456789abcdef0123456789abcdef",
				SpanID:  "1111111111111111",
				TagEntryList: []client.TagEntry{
					{Key: "http.status.code", Value: tc.value},
				},
			}}, nil, Options{})
			attrs := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()

			source, ok := attrs.Get("http.status.code")
			require.True(t, ok)
			require.Equal(t, tc.value, source.Str())

			status, ok := attrs.Get("http.response.status_code")
			require.Equal(t, tc.wantPresent, ok)
			if tc.wantPresent {
				require.Equal(t, pcommon.ValueTypeInt, status.Type())
				require.Equal(t, tc.wantValue, status.Int())
			}
		})
	}
}

func TestEnrichHTTPAttributesFromARMSShape(t *testing.T) {
	td := ToTraces([]client.Span{{
		TraceID:       "0123456789abcdef0123456789abcdef",
		SpanID:        "1111111111111111",
		RpcType:       0,
		OperationName: "/check/health",
		TagEntryList: []client.TagEntry{
			{Key: "http.status.code", Value: "200"},
			{Key: "source.ip", Value: "172.21.64.176"},
		},
	}}, nil, Options{})
	attrs := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()

	method, ok := attrs.Get("http.request.method")
	require.True(t, ok)
	require.Equal(t, "GET", method.Str())
	legacyMethod, ok := attrs.Get("http.method")
	require.True(t, ok)
	require.Equal(t, "GET", legacyMethod.Str())

	path, ok := attrs.Get("url.path")
	require.True(t, ok)
	require.Equal(t, "/check/health", path.Str())
	route, ok := attrs.Get("http.route")
	require.True(t, ok)
	require.Equal(t, "/check/health", route.Str())
}

func TestEnrichHTTPAttributesPrefersExistingTags(t *testing.T) {
	td := ToTraces([]client.Span{{
		TraceID:       "0123456789abcdef0123456789abcdef",
		SpanID:        "1111111111111111",
		RpcType:       0,
		OperationName: "/ignored",
		TagEntryList: []client.TagEntry{
			{Key: "http.request.method", Value: "POST"},
			{Key: "url.path", Value: "/v1/items"},
		},
	}}, nil, Options{})
	attrs := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()

	method, ok := attrs.Get("http.request.method")
	require.True(t, ok)
	require.Equal(t, "POST", method.Str())
	path, ok := attrs.Get("url.path")
	require.True(t, ok)
	require.Equal(t, "/v1/items", path.Str())
}

func TestEnrichHTTPAttributesFromMethodPrefixedOperation(t *testing.T) {
	td := ToTraces([]client.Span{{
		TraceID:       "0123456789abcdef0123456789abcdef",
		SpanID:        "1111111111111111",
		RpcType:       25,
		OperationName: "POST /v1/orders",
	}}, nil, Options{})
	attrs := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()

	method, ok := attrs.Get("http.request.method")
	require.True(t, ok)
	require.Equal(t, "POST", method.Str())
	path, ok := attrs.Get("url.path")
	require.True(t, ok)
	require.Equal(t, "/v1/orders", path.Str())
}

func TestEnrichHTTPAttributesSkipsNonHTTP(t *testing.T) {
	td := ToTraces([]client.Span{{
		TraceID:       "0123456789abcdef0123456789abcdef",
		SpanID:        "1111111111111111",
		RpcType:       60,
		OperationName: "SELECT",
	}}, nil, Options{})
	attrs := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()

	_, ok := attrs.Get("http.request.method")
	require.False(t, ok)
	_, ok = attrs.Get("url.path")
	require.False(t, ok)
}

func TestToTracesMapsStackAttributes(t *testing.T) {
	td := ToTraces([]client.Span{{
		TraceID: "0123456789abcdef0123456789abcdef",
		SpanID:  "1111111111111111",
		RpcID:   "0",
	}}, map[string][]client.StackFrame{
		"0": {
			{
				RpcID: "0", Api: "query", Line: "42", Exception: "query failed",
				ExtInfo: client.ExtInfo{Type: "20", Info: "SELECT 1"},
			},
		},
	}, Options{})
	span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, 1, span.Events().Len())

	attrs := span.Events().At(0).Attributes()
	function, ok := attrs.Get("code.function")
	require.True(t, ok)
	require.Equal(t, "query", function.Str())
	line, ok := attrs.Get("code.lineno")
	require.True(t, ok)
	require.EqualValues(t, 42, line.Int())
	exception, ok := attrs.Get("exception.message")
	require.True(t, ok)
	require.Equal(t, "query failed", exception.Str())
	statement, ok := attrs.Get("db.statement")
	require.True(t, ok)
	require.Equal(t, "SELECT 1", statement.Str())
}

func isEmptySpanID(id [8]byte) bool {
	for _, b := range id {
		if b != 0 {
			return false
		}
	}
	return true
}

func TestParseOddLengthTraceID(t *testing.T) {
	id, raw := parseTraceID("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa") // 30 hex chars padded
	require.False(t, isZeroTrace(id))
	_ = raw
}

func isZeroTrace(id [16]byte) bool {
	for _, b := range id {
		if b != 0 {
			return false
		}
	}
	return true
}
