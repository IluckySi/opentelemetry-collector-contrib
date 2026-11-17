// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package alibabacloudarmsreceiver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver/receivertest"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/client"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/mapper"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/metadata"
)

type fixtureAPIRequest struct {
	action  string
	traceID string
	rpcID   string
	service string
}

func TestReceiverWithHTTPFixtures(t *testing.T) {
	fixture := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join("testdata", name))
		require.NoError(t, err)
		return raw
	}
	searchFixture := fixture("search_traces.json")
	traceFixture := fixture("get_trace.json")
	stackFixture := fixture("get_stack.json")

	var searchResp client.SearchTracesResponse
	require.NoError(t, json.Unmarshal(searchFixture, &searchResp))
	require.Equal(t, "test-request", searchResp.RequestID)
	require.Len(t, searchResp.PageBean.TraceInfos, 1)
	traceID := searchResp.PageBean.TraceInfos[0].TraceID
	require.Equal(t, "test-frontend", searchResp.PageBean.TraceInfos[0].ServiceName)

	var traceResp client.GetTraceResponse
	require.NoError(t, json.Unmarshal(traceFixture, &traceResp))
	require.Equal(t, "test-trace-request", traceResp.RequestID)
	require.Len(t, traceResp.Spans, 2)
	for _, span := range traceResp.Spans {
		require.Equal(t, traceID, span.TraceID)
	}

	var stackResp client.GetStackResponse
	require.NoError(t, json.Unmarshal(stackFixture, &stackResp))
	require.Equal(t, "test-stack-request", stackResp.RequestID)

	var mu sync.Mutex
	var requests []fixtureAPIRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		values, err := url.ParseQuery(string(body))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		req := fixtureAPIRequest{
			action:  values.Get("Action"),
			traceID: values.Get("TraceID"),
			rpcID:   values.Get("RpcID"),
			service: values.Get("ServiceName"),
		}
		mu.Lock()
		requests = append(requests, req)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		switch req.action {
		case "SearchTracesByPage":
			_, _ = w.Write(searchFixture)
		case "GetTrace":
			_, _ = w.Write(traceFixture)
		case "GetStack":
			if req.traceID != traceID {
				http.Error(w, "unexpected trace ID", http.StatusBadRequest)
				return
			}
			// Only the root span has a recorded stack response; the child is mocked empty.
			if req.rpcID == "0" {
				_, _ = w.Write(stackFixture)
			} else {
				_, _ = w.Write([]byte(`{"asapiSuccess":true,"StackInfo":[]}`))
			}
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	serverURL, err := url.Parse(server.URL)
	require.NoError(t, err)
	cfg := validConfig()
	cfg.Endpoint = serverURL.Host
	cfg.Scheme = serverURL.Scheme
	cfg.Services = []string{"test-frontend"}
	cfg.Stack = StackConfig{Enabled: true, OnlyOnError: true, MaxCallsPerTrace: 2}
	sink := &consumertest.TracesSink{}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = client.New(client.Config{
		Endpoint:        cfg.Endpoint,
		Scheme:          cfg.Scheme,
		RegionID:        cfg.RegionID,
		APIVersion:      cfg.APIVersion,
		AccessKeyID:     cfg.AccessKeyID,
		AccessKeySecret: string(cfg.AccessKeySecret),
		OrganizationID:  cfg.OrganizationID,
		ResourceGroupID: cfg.ResourceGroupID,
		HTTPClient:      server.Client(),
	})

	require.NoError(t, r.pollOnce(context.Background()))
	require.Equal(t, 2, sink.SpanCount())
	require.Len(t, sink.AllTraces(), 1)

	spansByService := make(map[string]ptrace.Span)
	td := sink.AllTraces()[0]
	for i := 0; i < td.ResourceSpans().Len(); i++ {
		rs := td.ResourceSpans().At(i)
		service, ok := rs.Resource().Attributes().Get("service.name")
		require.True(t, ok)
		require.Equal(t, 1, rs.ScopeSpans().At(0).Spans().Len())
		spansByService[service.Str()] = rs.ScopeSpans().At(0).Spans().At(0)
	}
	require.Len(t, spansByService, 2)

	root := spansByService["test-frontend"]
	child := spansByService["test-backend"]
	require.Equal(t, "/api/example", root.Name())
	require.Equal(t, "/api/items", child.Name())

	rootSource := traceResp.Spans[0]
	rootTraceID, rootSpanID := mapper.SpanIDs(rootSource)
	require.Equal(t, rootTraceID, root.TraceID())
	require.Equal(t, rootSpanID, root.SpanID())
	childSource := traceResp.Spans[1]
	childTraceID, childSpanID := mapper.SpanIDs(childSource)
	require.Equal(t, childTraceID, child.TraceID())
	require.Equal(t, childSpanID, child.SpanID())
	require.Equal(t, root.SpanID(), child.ParentSpanID())
	require.True(t, isZeroSpanID(root.ParentSpanID()))

	for _, span := range []ptrace.Span{root, child} {
		require.Equal(t, ptrace.StatusCodeError, span.Status().Code())
		status, ok := span.Attributes().Get("http.response.status_code")
		require.True(t, ok)
		require.Equal(t, pcommon.ValueTypeInt, status.Type())
		require.EqualValues(t, 500, status.Int())
		result, ok := span.Attributes().Get("arms.result_code")
		require.True(t, ok)
		require.Equal(t, "1", result.Str())
		sourceIP, ok := span.Attributes().Get("source.ip")
		require.True(t, ok)
		require.NotEmpty(t, sourceIP.Str())
	}
	require.Equal(t, "192.0.2.12", mustStringAttribute(t, root.Attributes(), "source.ip"))
	require.Equal(t, 3, root.Events().Len())
	require.Equal(t, "arms.stack.frame", root.Events().At(0).Name())
	require.Equal(t, "Tomcat Servlet Process", mustStringAttribute(t, root.Events().At(0).Attributes(), "code.function"))
	require.Equal(t, "Server returned HTTP response code: 500 for URL: http://example.test/api/items",
		mustStringAttribute(t, root.Events().At(1).Attributes(), "exception.message"))
	require.Equal(t, "SELECT count(0) FROM demo_table", mustStringAttribute(t, root.Events().At(2).Attributes(), "db.statement"))
	require.Zero(t, child.Events().Len())

	mu.Lock()
	firstPollRequests := append([]fixtureAPIRequest(nil), requests...)
	mu.Unlock()
	require.Len(t, firstPollRequests, 4)
	require.Equal(t, "SearchTracesByPage", firstPollRequests[0].action)
	require.Equal(t, "test-frontend", firstPollRequests[0].service)
	require.Equal(t, "GetTrace", firstPollRequests[1].action)
	require.Equal(t, traceID, firstPollRequests[1].traceID)
	require.Equal(t, "GetStack", firstPollRequests[2].action)
	require.Equal(t, "0", firstPollRequests[2].rpcID)
	require.Equal(t, "GetStack", firstPollRequests[3].action)
	require.Equal(t, "0.1", firstPollRequests[3].rpcID)

	require.NoError(t, r.pollOnce(context.Background()))
	require.Equal(t, 2, sink.SpanCount())
	mu.Lock()
	allRequests := append([]fixtureAPIRequest(nil), requests...)
	mu.Unlock()
	require.Len(t, allRequests, 6)
	require.Equal(t, "SearchTracesByPage", allRequests[4].action)
	require.Equal(t, "GetTrace", allRequests[5].action)
	require.Equal(t, traceID, allRequests[5].traceID)
	require.Equal(t, 2, countFixtureRequests(allRequests, "GetStack"))
}

func isZeroSpanID(id pcommon.SpanID) bool {
	return id == (pcommon.SpanID{})
}

func mustStringAttribute(t *testing.T, attrs pcommon.Map, key string) string {
	t.Helper()
	value, ok := attrs.Get(key)
	require.True(t, ok, "missing attribute %q", key)
	require.Equal(t, pcommon.ValueTypeStr, value.Type())
	return value.Str()
}

func countFixtureRequests(requests []fixtureAPIRequest, action string) int {
	count := 0
	for _, request := range requests {
		if request.action == action {
			count++
		}
	}
	return count
}
