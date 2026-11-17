// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mapper

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/client"
)

func TestSpanKindFromRpcType(t *testing.T) {
	tests := []struct {
		name  string
		types []int
		want  ptrace.SpanKind
	}{
		{"server", []int{0, 2, 8, 9, 12, 16, 18, 102, 1301, 1302, 1303, 1304, 1305, 1308}, ptrace.SpanKindServer},
		{"client", []int{1, 4, 5, 7, 10, 11, 17, 19, 25, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 101, 105}, ptrace.SpanKindClient},
		{"producer", []int{3, 13, 103}, ptrace.SpanKindProducer},
		{"consumer", []int{252, 254, 104}, ptrace.SpanKindConsumer},
		{"internal", []int{40, 98}, ptrace.SpanKindInternal},
		{"unspecified", []int{-3, -2, -1, 23, 41, 100, 106, 256}, ptrace.SpanKindUnspecified},
		{"undocumented", []int{1306, 9999}, ptrace.SpanKindUnspecified},
	}
	for _, tt := range tests {
		for _, rpcType := range tt.types {
			t.Run(fmt.Sprintf("%s/%d", tt.name, rpcType), func(t *testing.T) {
				require.Equal(t, tt.want, spanKindFromRpcType(rpcType, "operation"))
			})
		}
	}
}

func TestKafkaOperationHints(t *testing.T) {
	for _, rpcType := range []int{23, 256} {
		require.Equal(t, ptrace.SpanKindProducer, spanKindFromRpcType(rpcType, "Send topic orders"))
		require.Equal(t, ptrace.SpanKindConsumer, spanKindFromRpcType(rpcType, "Recv topic orders"))
		require.Equal(t, ptrace.SpanKindUnspecified, spanKindFromRpcType(rpcType, "orders"))
	}
	require.Equal(t, ptrace.SpanKindServer, spanKindFromRpcType(0, "send topic"))
	require.Equal(t, ptrace.SpanKindProducer, spanKindFromRpcType(13, "recv topic"))
}

func TestSpanKindDirectionHints(t *testing.T) {
	tests := []struct {
		name      string
		rpcType   int
		operation string
		parentID  string
		tags      []client.TagEntry
		want      ptrace.SpanKind
	}{
		{"explicit client", -1, "", "", []client.TagEntry{{Key: "span.kind", Value: " CLIENT "}}, ptrace.SpanKindClient},
		{"explicit server", 100, "", "", []client.TagEntry{{Key: "span.kind", Value: "server"}}, ptrace.SpanKindServer},
		{"explicit internal", 41, "", "", []client.TagEntry{{Key: "span.kind", Value: "internal"}}, ptrace.SpanKindInternal},
		{"explicit producer", 23, "", "", []client.TagEntry{{Key: "span.kind", Value: "producer"}}, ptrace.SpanKindProducer},
		{"explicit consumer", 256, "", "", []client.TagEntry{{Key: "span.kind", Value: "consumer"}}, ptrace.SpanKindConsumer},
		{"known type wins", 0, "", "", []client.TagEntry{{Key: "span.kind", Value: "client"}}, ptrace.SpanKindServer},
		{"tag beats operation", 23, "send topic", "", []client.TagEntry{{Key: "span.kind", Value: "consumer"}}, ptrace.SpanKindConsumer},
		{"invalid tag", 23, "send topic", "", []client.TagEntry{{Key: "span.kind", Value: "invalid"}}, ptrace.SpanKindUnspecified},
		{"conflicting tags", -1, "", "", []client.TagEntry{{Key: "span.kind", Value: "server"}, {Key: "span.kind", Value: "client"}}, ptrace.SpanKindUnspecified},
		{"same tags", -1, "", "", []client.TagEntry{{Key: "span.kind", Value: "SERVER"}, {Key: "span.kind", Value: "server"}}, ptrace.SpanKindServer},
		{"message create", 23, "", "", []client.TagEntry{{Key: "messaging.operation.type", Value: "create"}}, ptrace.SpanKindProducer},
		{"message process", 256, "", "", []client.TagEntry{{Key: "messaging.operation.type", Value: "process"}}, ptrace.SpanKindConsumer},
		{"message receive", 256, "", "", []client.TagEntry{{Key: "messaging.operation.type", Value: "receive"}}, ptrace.SpanKindClient},
		{"message settle", 256, "", "", []client.TagEntry{{Key: "messaging.operation.type", Value: "settle"}}, ptrace.SpanKindClient},
		{"message send ambiguous", 23, "send topic", "", []client.TagEntry{{Key: "messaging.operation.type", Value: "send"}}, ptrace.SpanKindUnspecified},
		{"conflicting operations", -1, "", "", []client.TagEntry{{Key: "messaging.operation.type", Value: "create"}, {Key: "messaging.operation.type", Value: "process"}}, ptrace.SpanKindUnspecified},
		{"kafka name fallback", 23, "send topic orders", "", nil, ptrace.SpanKindProducer},
		{"root not necessarily server", 100, "", "", nil, ptrace.SpanKindUnspecified},
		{"child not necessarily internal", -1, "", "1111111111111111", nil, ptrace.SpanKindUnspecified},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			td := ToTraces([]client.Span{{
				TraceID:       "0123456789abcdef0123456789abcdef",
				SpanID:        "2222222222222222",
				ParentSpanID:  tt.parentID,
				RpcType:       tt.rpcType,
				OperationName: tt.operation,
				TagEntryList:  tt.tags,
			}}, nil, Options{})
			span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
			require.Equal(t, tt.want, span.Kind())
		})
	}
}

func TestUnknownRpcTypeIsPreserved(t *testing.T) {
	td := ToTraces([]client.Span{{
		TraceID: "0123456789abcdef0123456789abcdef",
		SpanID:  "1111111111111111",
		RpcType: 9999,
		TagEntryList: []client.TagEntry{
			{Key: "arms.rpc_type", Value: "incorrect"},
		},
	}}, nil, Options{})
	span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, ptrace.SpanKindUnspecified, span.Kind())
	value, ok := span.Attributes().Get("arms.rpc_type")
	require.True(t, ok)
	require.EqualValues(t, 9999, value.Int())
}
