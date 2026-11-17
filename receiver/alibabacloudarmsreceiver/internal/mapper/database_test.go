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

func TestDatabaseRpcTypes(t *testing.T) {
	tests := []struct {
		rpcType int
		system  string
	}{
		{4, ""}, {5, "tair"}, {52, "ibm.db2"}, {53, "ibm.informix"},
		{54, "sequoiadb"}, {55, "gbase"}, {56, "gaussdb"}, {57, "kingbase"},
		{58, "influxdb"}, {59, "clickhouse"}, {60, "mysql"}, {61, "mysql"},
		{62, "oracle.db"}, {63, "postgresql"}, {64, "mongodb"}, {65, "ppas"},
		{66, "microsoft.sql_server"}, {67, "mariadb"}, {68, "dmdb"},
		{69, "oceanbase"}, {70, "redis"}, {71, "memcached"}, {72, "elasticsearch"},
		{73, "kudu"}, {105, ""},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.rpcType), func(t *testing.T) {
			td := ToTraces([]client.Span{{
				TraceID:     "0123456789abcdef0123456789abcdef",
				SpanID:      "1111111111111111",
				ServiceName: "test-service",
				ServiceIP:   "192.0.2.1",
				RpcType:     tt.rpcType,
			}}, nil, Options{})
			span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
			require.Equal(t, ptrace.SpanKindClient, span.Kind())
			attrs := span.Attributes()
			sourceType, ok := attrs.Get("arms.rpc_type")
			require.True(t, ok)
			require.EqualValues(t, tt.rpcType, sourceType.Int())
			system, ok := attrs.Get("db.system.name")
			if tt.system == "" {
				require.False(t, ok)
			} else {
				require.True(t, ok)
				require.Equal(t, tt.system, system.Str())
			}
			_, ok = attrs.Get("server.address")
			require.False(t, ok, "application IP must not be used as the database address")
			_, ok = attrs.Get("db.namespace")
			require.False(t, ok)
		})
	}
}

func TestDatabaseExplicitAttributesTakePrecedence(t *testing.T) {
	td := ToTraces([]client.Span{{
		TraceID: "0123456789abcdef0123456789abcdef",
		SpanID:  "1111111111111111",
		RpcType: 60,
		TagEntryList: []client.TagEntry{
			{Key: "db.system.name", Value: "mariadb"},
			{Key: "db.namespace", Value: "inventory"},
			{Key: "server.address", Value: "db.example.test"},
		},
	}}, nil, Options{})
	attrs := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0).Attributes()
	for key, expected := range map[string]string{
		"db.system.name": "mariadb",
		"db.namespace":   "inventory",
		"server.address": "db.example.test",
	} {
		value, ok := attrs.Get(key)
		require.True(t, ok)
		require.Equal(t, expected, value.Str())
	}
}

func TestNonDatabaseDoesNotGetInferredDatabaseSystem(t *testing.T) {
	td := ToTraces([]client.Span{{
		TraceID: "0123456789abcdef0123456789abcdef",
		SpanID:  "1111111111111111",
		RpcType: 9999,
	}}, nil, Options{})
	span := td.ResourceSpans().At(0).ScopeSpans().At(0).Spans().At(0)
	require.Equal(t, ptrace.SpanKindUnspecified, span.Kind())
	_, ok := span.Attributes().Get("db.system.name")
	require.False(t, ok)
}
