// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package mapper // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/mapper"

import (
	"strings"

	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/client"
)

// spanKind uses source direction tags only when RpcType does not determine a kind.
func spanKind(span client.Span) ptrace.SpanKind {
	kind := spanKindFromRpcType(span.RpcType, "")
	if kind != ptrace.SpanKindUnspecified {
		return kind
	}

	var explicit ptrace.SpanKind
	hasExplicit := false
	for _, tag := range span.TagEntryList {
		if tag.Key != "span.kind" {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(tag.Value))
		var candidate ptrace.SpanKind
		switch value {
		case "server":
			candidate = ptrace.SpanKindServer
		case "client":
			candidate = ptrace.SpanKindClient
		case "producer":
			candidate = ptrace.SpanKindProducer
		case "consumer":
			candidate = ptrace.SpanKindConsumer
		case "internal":
			candidate = ptrace.SpanKindInternal
		default:
			return ptrace.SpanKindUnspecified
		}
		if hasExplicit && explicit != candidate {
			return ptrace.SpanKindUnspecified
		}
		explicit, hasExplicit = candidate, true
	}
	if hasExplicit {
		return explicit
	}

	var operationKind ptrace.SpanKind
	hasOperation := false
	for _, tag := range span.TagEntryList {
		if tag.Key != "messaging.operation.type" {
			continue
		}
		var candidate ptrace.SpanKind
		switch strings.ToLower(strings.TrimSpace(tag.Value)) {
		case "create":
			candidate = ptrace.SpanKindProducer
		case "process":
			candidate = ptrace.SpanKindConsumer
		case "receive", "settle":
			candidate = ptrace.SpanKindClient
		default:
			// A send operation may be either a client or a producer span.
			return ptrace.SpanKindUnspecified
		}
		if hasOperation && operationKind != candidate {
			return ptrace.SpanKindUnspecified
		}
		operationKind, hasOperation = candidate, true
	}
	if hasOperation {
		return operationKind
	}

	return spanKindFromRpcType(span.RpcType, span.OperationName)
}

// spanKindFromRpcType maps documented EagleEye RpcType values to OTLP SpanKind.
// https://github.com/aliyun/aliyun-openapi-meta/blob/8095c38507e858189b96974f7dce2914abd108a6/canonical/arms/2019-08-08/GetTrace.json
// https://github.com/alibabacloud-go/arms-20190808/blob/a83c60a650e2ca102a355283dd37a87d8d33aef7/client/get_trace_response_body_model.go
func spanKindFromRpcType(rpcType int, operationName string) ptrace.SpanKind {
	switch rpcType {
	// 0 http, 2 hsf, 8 dubbo, 9 grpc, 12 dsf, 16 thrift, 18 sofa, 102 server.
	case 0, 2, 8, 9, 12, 16, 18, 102:
		return ptrace.SpanKindServer
	// 1301 schedulerx, 1302 XXL_Job, 1303 Spring_Scheduled, 1304 Quartz, 1305 ElasticJob, 1308 Jdk_Timer.
	case 1301, 1302, 1303, 1304, 1305, 1308:
		return ptrace.SpanKindServer
	// 1 hsf_client, 7 dubbo_client, 10 grpc_client, 11 dsf_client, 17 thrift_client,
	// 19 sofa_client, 25 http_client, 101 client.
	case 1, 7, 10, 11, 17, 19, 25, 101:
		return ptrace.SpanKindClient
	// 4 tddl, 5 tair, 52 DB2, 53 Informix, 54 SequoiaDB, 55 Gbase, 56 GaussDB,
	// 57 KingBase, 58 infuxdb, 59 clickhouse, 60 mysql, 61 mysql, 62 oracle,
	// 63 postgresql, 64 mongodb, 65 ppas, 66 sqlserver, 67 mariadb, 68 dmdb,
	// 69 oceanbase, 70 redis, 71 memcached, 72 elasticsearch, 73 kudu, 105 db.
	// Treat these as client operations; most names do not specify direction.
	case 4, 5, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 105:
		return ptrace.SpanKindClient
	case 3, 13, 103: // 3 notify_client, 13 mq_client, 103 producer
		return ptrace.SpanKindProducer
	case 252, 254, 104: // 252 mq, 254 notify, 104 consumer
		return ptrace.SpanKindConsumer
	case 40, 98: // 40 local, 98 user_method
		return ptrace.SpanKindInternal
	case 23, 256: // 23 kafka_client, 256 kafka: direction is not documented
		op := strings.ToLower(operationName)
		if strings.Contains(op, "send topic") || strings.HasPrefix(op, "send ") {
			return ptrace.SpanKindProducer
		}
		if strings.Contains(op, "recv topic") || strings.HasPrefix(op, "recv ") {
			return ptrace.SpanKindConsumer
		}
		return ptrace.SpanKindUnspecified
	case -3, -2, -1, 41, 100, 106: // -3 app, -2 front, -1 unknown, 41 async, 100 root, 106 xtrace_other
		return ptrace.SpanKindUnspecified
	default:
		return ptrace.SpanKindUnspecified
	}
}
