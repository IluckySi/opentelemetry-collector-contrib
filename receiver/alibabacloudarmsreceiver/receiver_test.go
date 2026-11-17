// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package alibabacloudarmsreceiver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.opentelemetry.io/collector/receiver/receivertest"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/client"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/mapper"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/metadata"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/poller"
)

type mockAPI struct {
	mu            sync.Mutex
	searches      int
	searchWindows [][2]int64
	getTraceCalls int
	getTraceFail  map[string]int // fail first N GetTrace calls per id
	traces        map[string][]client.Span
}

type splitWindowMock struct {
	mockAPI
	startMs int64
	endMs   int64
	midMs   int64
}

func (m *splitWindowMock) SearchTracesByPage(_ context.Context, _ string, startMs, endMs int64, pageNumber, _ int) (*client.SearchTracesResponse, error) {
	m.mu.Lock()
	m.searches++
	m.mu.Unlock()

	traceID := ""
	switch {
	case pageNumber == 1 && startMs == m.startMs && endMs == m.endMs:
		traceID = "trace-a"
	case pageNumber == 2 && startMs == m.startMs && endMs == m.endMs:
		traceID = "trace-b"
	case pageNumber == 1 && endMs <= m.midMs:
		traceID = "trace-a"
	case pageNumber == 1 && startMs >= m.midMs:
		traceID = "trace-b"
	}
	if traceID == "" {
		return &client.SearchTracesResponse{AsapiSuccess: true}, nil
	}
	return &client.SearchTracesResponse{
		AsapiSuccess: true,
		PageBean: client.PageBean{
			TraceInfos: []client.TraceInfo{{TraceID: traceID}},
		},
	}, nil
}

func (m *mockAPI) SearchTracesByPage(_ context.Context, serviceName string, startMs, endMs int64, pageNumber, _ int) (*client.SearchTracesResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.searches++
	m.searchWindows = append(m.searchWindows, [2]int64{startMs, endMs})
	if pageNumber > 1 {
		return &client.SearchTracesResponse{AsapiSuccess: true}, nil
	}
	infos := []client.TraceInfo{}
	for tid, spans := range m.traces {
		svc := serviceName
		if len(spans) > 0 {
			svc = spans[0].ServiceName
		}
		infos = append(infos, client.TraceInfo{TraceID: tid, ServiceName: svc, Duration: 10, Timestamp: time.Now().UnixMilli()})
	}
	return &client.SearchTracesResponse{
		AsapiSuccess: true,
		PageBean:     client.PageBean{TraceInfos: infos, PageSize: 100, PageNumber: 1, Total: len(infos)},
	}, nil
}

func TestPollServiceDelayedQueryWindows(t *testing.T) {
	for _, overlap := range []time.Duration{0, 30 * time.Second} {
		t.Run(overlap.String(), func(t *testing.T) {
			cfg := validConfig()
			cfg.QueryDelay = 3 * time.Minute
			cfg.OverlapSlack = overlap
			mock := &mockAPI{}
			r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, &consumertest.TracesSink{})
			r.client = mock
			now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)

			require.NoError(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})))
			require.Equal(t, [2]int64{now.Add(-8 * time.Minute).UnixMilli(), now.Add(-3 * time.Minute).UnixMilli()}, mock.searchWindows[0])

			require.NoError(t, r.pollService(t.Context(), "svc-a", now.Add(time.Minute), make(map[string]struct{})))
			require.Equal(t, [2]int64{now.Add(-3*time.Minute - overlap).UnixMilli(), now.Add(-2 * time.Minute).UnixMilli()}, mock.searchWindows[1])
		})
	}
}

func TestPollServiceDelayedCatchUp(t *testing.T) {
	cfg := validConfig()
	cfg.QueryDelay = 3 * time.Minute
	cfg.OverlapSlack = 30 * time.Second
	mock := &mockAPI{}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, &consumertest.TracesSink{})
	r.client = mock
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	start := now.Add(-time.Hour)
	r.state.SetWatermark("svc-a", start)

	require.NoError(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})))
	require.Equal(t, [2]int64{start.UnixMilli(), start.Add(30 * time.Minute).UnixMilli()}, mock.searchWindows[0])
	require.Equal(t, start.Add(30*time.Minute), r.state.Watermark("svc-a"))
}

func TestPollServiceDelayedEndBeforeWatermark(t *testing.T) {
	cfg := validConfig()
	cfg.QueryDelay = 3 * time.Minute
	mock := &mockAPI{}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, &consumertest.TracesSink{})
	r.client = mock
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	r.state.SetWatermark("svc-a", now)

	require.NoError(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})))
	require.Empty(t, mock.searchWindows)
	require.Equal(t, now, r.state.Watermark("svc-a"))
}

func TestPollServiceDebugSummary(t *testing.T) {
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	traceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	for _, tt := range []struct {
		name        string
		watermark   time.Time
		traceFail   bool
		overlap     time.Duration
		result      string
		start       time.Time
		end         time.Time
		after       time.Time
		initialized bool
		attempted   bool
		advanced    bool
	}{
		{
			name: "initial success", result: "success",
			start: now.Add(-8 * time.Minute), end: now.Add(-3 * time.Minute), after: now.Add(-3 * time.Minute),
			initialized: true, attempted: true, advanced: true,
		},
		{
			name: "initial failure", traceFail: true, result: "failed",
			start: now.Add(-8 * time.Minute), end: now.Add(-3 * time.Minute), after: now.Add(-8 * time.Minute),
			initialized: true, attempted: true,
		},
		{
			name: "existing watermark failure", watermark: now.Add(-4 * time.Minute), traceFail: true, result: "failed",
			start: now.Add(-4 * time.Minute), end: now.Add(-3 * time.Minute), after: now.Add(-4 * time.Minute),
			attempted: true,
		},
		{
			name: "segmented success", watermark: now.Add(-time.Hour), result: "success",
			start: now.Add(-time.Hour), end: now.Add(-30 * time.Minute), after: now.Add(-30 * time.Minute),
			attempted: true, advanced: true,
		},
		{
			name: "overlap prevents advance", watermark: now.Add(-4 * time.Minute), overlap: 2 * time.Minute, result: "success",
			start: now.Add(-4 * time.Minute), end: now.Add(-3 * time.Minute), after: now.Add(-4 * time.Minute),
			attempted: true,
		},
		{
			name: "skipped", watermark: now, result: "skipped",
			start: now, end: now.Add(-3 * time.Minute), after: now,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validConfig()
			cfg.QueryDelay = 3 * time.Minute
			cfg.OverlapSlack = tt.overlap
			core, logs := observer.New(zap.DebugLevel)
			settings := receivertest.NewNopSettings(metadata.Type)
			settings.Logger = zap.New(core)
			r := newReceiver(settings, cfg, &consumertest.TracesSink{})
			mock := &mockAPI{}
			if tt.traceFail {
				mock.traces = map[string][]client.Span{traceID: sampleSpans(traceID)}
				mock.getTraceFail = map[string]int{traceID: 1}
			}
			r.client = mock
			if !tt.watermark.IsZero() {
				r.state.SetWatermark("svc-a", tt.watermark)
			}
			err := r.pollService(t.Context(), "svc-a", now, make(map[string]struct{}))
			if tt.traceFail {
				require.ErrorIs(t, err, errPartialExport)
			} else {
				require.NoError(t, err)
			}
			entries := logs.FilterMessage("ARMS service poll summary").All()
			require.Len(t, entries, 1)
			fields := entries[0].ContextMap()
			require.Equal(t, "svc-a", fields["service"])
			require.Equal(t, tt.result, fields["result"])
			require.Equal(t, tt.attempted, fields["query_attempted"])
			require.Equal(t, tt.start.UnixMilli(), fields["start_ms"])
			require.Equal(t, tt.end.UnixMilli(), fields["end_ms"])
			require.Equal(t, tt.initialized, fields["watermark_initialized"])
			require.Equal(t, tt.start, fields["watermark_before"])
			require.Equal(t, tt.after, fields["watermark_after"])
			require.Equal(t, tt.advanced, fields["watermark_advanced"])
			require.Equal(t, tt.after, r.state.Watermark("svc-a"))
			require.Equal(t, tt.attempted, len(mock.searchWindows) > 0)
		})
	}
}

func TestPollServiceDelayedWindowFailureKeepsWatermark(t *testing.T) {
	cfg := validConfig()
	cfg.QueryDelay = 3 * time.Minute
	traceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mock := &mockAPI{
		traces:       map[string][]client.Span{traceID: sampleSpans(traceID)},
		getTraceFail: map[string]int{traceID: 1},
	}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, &consumertest.TracesSink{})
	r.client = mock
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	start := now.Add(-4 * time.Minute)
	r.state.SetWatermark("svc-a", start)

	require.ErrorIs(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})), errPartialExport)
	require.Equal(t, start, r.state.Watermark("svc-a"))
	require.Equal(t, [2]int64{start.UnixMilli(), now.Add(-3 * time.Minute).UnixMilli()}, mock.searchWindows[0])
}

func TestInitialFailurePreservesQueryStart(t *testing.T) {
	cfg := validConfig()
	cfg.QueryDelay = 3 * time.Minute
	traceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mock := &mockAPI{
		traces:       map[string][]client.Span{traceID: sampleSpans(traceID)},
		getTraceFail: map[string]int{traceID: 2},
	}
	sink := &consumertest.TracesSink{}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	start := now.Add(-cfg.QueryDelay - initialLookback)

	for _, delay := range []time.Duration{0, 6 * time.Minute} {
		require.ErrorIs(t, r.pollService(t.Context(), "svc-a", now.Add(delay), make(map[string]struct{})), errPartialExport)
		require.Equal(t, start, r.state.Watermark("svc-a"))
	}
	require.NoError(t, r.pollService(t.Context(), "svc-a", now.Add(7*time.Minute), make(map[string]struct{})))
	require.Equal(t, 2, sink.SpanCount())
	for _, window := range mock.searchWindows {
		require.Equal(t, start.UnixMilli(), window[0])
	}
	require.Equal(t, now.Add(7*time.Minute-cfg.QueryDelay), r.state.Watermark("svc-a"))
}

func (m *mockAPI) GetTrace(_ context.Context, traceID string) (*client.GetTraceResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.getTraceCalls++
	if n := m.getTraceFail[traceID]; n > 0 {
		m.getTraceFail[traceID] = n - 1
		return nil, errors.New("transient gettrace failure")
	}
	return &client.GetTraceResponse{AsapiSuccess: true, Spans: m.traces[traceID]}, nil
}

func spanKey(span client.Span) poller.SpanKey {
	traceID, spanID := mapper.SpanIDs(span)
	return poller.SpanKey{TraceID: traceID, SpanID: spanID}
}

func (m *mockAPI) GetStack(context.Context, string, string) (*client.GetStackResponse, error) {
	return &client.GetStackResponse{AsapiSuccess: true}, nil
}

func sampleSpans(traceID string) []client.Span {
	return []client.Span{
		{
			TraceID: traceID, SpanID: "1111111111111111", ParentSpanID: "",
			ServiceName: "svc-a", OperationName: "/a", RpcID: "0", ResultCode: "0",
			Timestamp: time.Now().UnixMilli(), Duration: 5,
		},
		{
			TraceID: traceID, SpanID: "2222222222222222", ParentSpanID: "1111111111111111",
			ServiceName: "svc-b", OperationName: "/b", RpcID: "0.1", ResultCode: "0",
			Timestamp: time.Now().UnixMilli(), Duration: 3,
		},
	}
}

func TestReceiverPollDedupesAcrossServices(t *testing.T) {
	sink := &consumertest.TracesSink{}
	cfg := validConfig()
	cfg.Services = []string{"svc-a", "svc-b"}
	cfg.InitialDelay = 0
	cfg.CollectionInterval = time.Hour
	cfg.OverlapSlack = 0

	traceID := "0123456789abcdef0123456789abcdef"
	mock := &mockAPI{traces: map[string][]client.Span{traceID: sampleSpans(traceID)}}

	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock

	require.NoError(t, r.Start(t.Context(), componenttest.NewNopHost()))
	defer func() { require.NoError(t, r.Shutdown(t.Context())) }()

	require.Eventually(t, func() bool {
		return sink.SpanCount() > 0
	}, 3*time.Second, 20*time.Millisecond)

	require.Equal(t, 2, sink.SpanCount())
	require.Equal(t, 1, len(sink.AllTraces()))

	var names []string
	td := sink.AllTraces()[0]
	for i := 0; i < td.ResourceSpans().Len(); i++ {
		rs := td.ResourceSpans().At(i)
		v, _ := rs.Resource().Attributes().Get("service.name")
		names = append(names, v.Str())
	}
	require.ElementsMatch(t, []string{"svc-a", "svc-b"}, names)
}

func TestPollOnceExportsLateSpansAndQueriesEachTraceOncePerRound(t *testing.T) {
	sink := &consumertest.TracesSink{}
	cfg := validConfig()
	cfg.Services = []string{"svc-a", "svc-b"}
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	spans := sampleSpans(traceID)
	mock := &mockAPI{traces: map[string][]client.Span{traceID: {spans[0], spans[0]}}}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock

	require.NoError(t, r.pollOnce(t.Context()))
	require.Equal(t, 1, mock.getTraceCalls)
	require.Equal(t, 1, sink.SpanCount())

	mock.traces[traceID] = spans
	require.NoError(t, r.pollOnce(t.Context()))
	require.Equal(t, 2, mock.getTraceCalls)
	require.Equal(t, 2, sink.SpanCount())
	require.Len(t, sink.AllTraces(), 2)

	require.NoError(t, r.pollOnce(t.Context()))
	require.Equal(t, 3, mock.getTraceCalls)
	require.Equal(t, 2, sink.SpanCount())
}

func TestSpanDeduplicationIncludesTraceID(t *testing.T) {
	sink := &consumertest.TracesSink{}
	cfg := validConfig()
	firstID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	secondID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mock := &mockAPI{traces: map[string][]client.Span{
		firstID:  sampleSpans(firstID)[:1],
		secondID: sampleSpans(secondID)[:1],
	}}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock

	require.NoError(t, r.pollOnce(t.Context()))
	require.Equal(t, 2, sink.SpanCount())
}

func TestMismatchedTraceDetailsDoNotAdvanceWatermark(t *testing.T) {
	cfg := validConfig()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	otherTraceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mock := &mockAPI{traces: map[string][]client.Span{traceID: sampleSpans(otherTraceID)}}
	sink := &consumertest.TracesSink{}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock
	now := time.Now()

	require.ErrorIs(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})), errPartialExport)
	require.Zero(t, sink.SpanCount())
	require.Equal(t, now.Add(-cfg.QueryDelay-initialLookback), r.state.Watermark("svc-a"))
}

func TestEmptyTraceCanBeRetrievedInLaterRound(t *testing.T) {
	sink := &consumertest.TracesSink{}
	cfg := validConfig()
	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mock := &mockAPI{traces: map[string][]client.Span{traceID: nil}}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock

	require.NoError(t, r.pollOnce(t.Context()))
	require.Zero(t, sink.SpanCount())
	mock.traces[traceID] = sampleSpans(traceID)
	require.NoError(t, r.pollOnce(t.Context()))
	require.Equal(t, 2, sink.SpanCount())
}

func TestExportFailureRetriesAndDoesNotMarkSeen(t *testing.T) {
	sink := &consumertest.TracesSink{}
	cfg := validConfig()
	cfg.Services = []string{"svc-a"}
	cfg.InitialDelay = 0
	cfg.CollectionInterval = time.Hour
	cfg.OverlapSlack = 0

	traceID := "deadbeefdeadbeefdeadbeefdeadbeef"
	mock := &mockAPI{
		traces:       map[string][]client.Span{traceID: sampleSpans(traceID)},
		getTraceFail: map[string]int{traceID: 1},
	}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock

	now := time.Now()
	require.Error(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})))
	require.Equal(t, 0, sink.SpanCount())
	require.False(t, r.state.HasSeen(spanKey(sampleSpans(traceID)[0])))
	require.Equal(t, now.Add(-cfg.QueryDelay-initialLookback), r.state.Watermark("svc-a"))

	// Retry succeeds and advances watermark.
	require.NoError(t, r.pollService(t.Context(), "svc-a", time.Now(), make(map[string]struct{})))
	require.Equal(t, 2, sink.SpanCount())
	require.True(t, r.state.HasSeen(spanKey(sampleSpans(traceID)[0])))
	require.False(t, r.state.Watermark("svc-a").IsZero())
}

func TestSegmentedLookbackCatchUp(t *testing.T) {
	sink := &consumertest.TracesSink{}
	cfg := validConfig()
	cfg.Services = []string{"svc-a"}
	cfg.OverlapSlack = 0

	traceID := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mock := &mockAPI{traces: map[string][]client.Span{traceID: sampleSpans(traceID)}}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock

	now := time.Now()
	// Simulate being 1h behind.
	r.state.SetWatermark("svc-a", now.Add(-time.Hour))

	require.NoError(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})))
	wm := r.state.Watermark("svc-a")
	// Advanced by one bounded segment from the old watermark, not jumped to now.
	require.WithinDuration(t, now.Add(-time.Hour).Add(30*time.Minute), wm, time.Second)
	require.True(t, wm.Before(now))
}

func TestPollServiceSplitsWindowWhenPageLimitIsExceeded(t *testing.T) {
	sink := &consumertest.TracesSink{}
	cfg := validConfig()
	cfg.Page.PageSize = 1
	cfg.Page.MaxPage = 1
	cfg.OverlapSlack = 0

	now := time.Now().Truncate(time.Millisecond)
	start := now.Add(-4 * time.Second)
	startMs := start.UnixMilli()
	endMs := now.UnixMilli()
	midMs := startMs + (endMs-startMs)/2
	mock := &splitWindowMock{
		mockAPI: mockAPI{
			traces: map[string][]client.Span{
				"trace-a": sampleSpans("trace-a"),
				"trace-b": sampleSpans("trace-b"),
			},
		},
		startMs: startMs,
		endMs:   endMs,
		midMs:   midMs,
	}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock
	r.state.SetWatermark("svc-a", start)

	require.NoError(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})))
	require.Equal(t, 4, sink.SpanCount())
	require.Greater(t, mock.searches, 2)
	require.Equal(t, now, r.state.Watermark("svc-a"))
}

func TestPollServiceAdvancesWhenFullLastPageHasNoNextPage(t *testing.T) {
	sink := &consumertest.TracesSink{}
	cfg := validConfig()
	cfg.Page.PageSize = 1
	cfg.Page.MaxPage = 1
	cfg.OverlapSlack = 0
	traceID := "one-full-page"
	mock := &mockAPI{
		traces:       map[string][]client.Span{traceID: sampleSpans(traceID)},
		getTraceFail: map[string]int{},
	}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, sink)
	r.client = mock
	now := time.Now().Truncate(time.Millisecond)

	require.NoError(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})))
	require.Equal(t, 2, mock.searches)
	require.Equal(t, 2, sink.SpanCount())
	require.Equal(t, now, r.state.Watermark("svc-a"))
}

func TestEmptyGetTraceIsSkipped(t *testing.T) {
	cfg := validConfig()
	cfg.OverlapSlack = 0
	traceID := "empty-trace"
	mock := &mockAPI{
		traces:       map[string][]client.Span{traceID: nil},
		getTraceFail: map[string]int{},
	}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, &consumertest.TracesSink{})
	r.client = mock

	now := time.Now()
	queried := make(map[string]struct{})
	require.NoError(t, r.pollService(t.Context(), "svc-a", now, queried))
	require.Contains(t, queried, traceID)
	require.WithinDuration(t, now, r.state.Watermark("svc-a"), time.Millisecond)
}

type failConsumer struct {
	n int
}

func (f *failConsumer) ConsumeTraces(context.Context, ptrace.Traces) error {
	f.n++
	return errors.New("downstream full")
}

func (f *failConsumer) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{}
}

func TestConsumeFailureDoesNotMarkSeen(t *testing.T) {
	fc := &failConsumer{}
	cfg := validConfig()
	cfg.Services = []string{"svc-a"}
	cfg.OverlapSlack = 0

	traceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mock := &mockAPI{traces: map[string][]client.Span{traceID: sampleSpans(traceID)}}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, fc)
	r.client = mock

	now := time.Now()
	require.Error(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})))
	require.Equal(t, 1, fc.n)
	require.False(t, r.state.HasSeen(spanKey(sampleSpans(traceID)[0])))
	require.Equal(t, now.Add(-cfg.QueryDelay-initialLookback), r.state.Watermark("svc-a"))

	sink := &consumertest.TracesSink{}
	r.nextConsumer = sink
	require.NoError(t, r.pollService(t.Context(), "svc-a", time.Now(), make(map[string]struct{})))
	require.Equal(t, 2, sink.SpanCount())
	require.True(t, r.state.HasSeen(spanKey(sampleSpans(traceID)[0])))
}

func TestFailedSpanExportBeforeEmptyNextPageDoesNotAdvanceWatermark(t *testing.T) {
	cfg := validConfig()
	cfg.Page.PageSize = 1
	cfg.Page.MaxPage = 2
	traceID := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mock := &mockAPI{
		traces:       map[string][]client.Span{traceID: sampleSpans(traceID)},
		getTraceFail: map[string]int{traceID: 1},
	}
	r := newReceiver(receivertest.NewNopSettings(metadata.Type), cfg, &consumertest.TracesSink{})
	r.client = mock

	now := time.Now()
	require.ErrorIs(t, r.pollService(t.Context(), "svc-a", now, make(map[string]struct{})), errPartialExport)
	require.Equal(t, now.Add(-cfg.QueryDelay-initialLookback), r.state.Watermark("svc-a"))
}
