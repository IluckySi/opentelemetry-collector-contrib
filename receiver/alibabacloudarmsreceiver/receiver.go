// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package alibabacloudarmsreceiver // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver"

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/receiver"
	"go.uber.org/zap"

	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/client"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/mapper"
	"github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/poller"
)

var errPartialExport = errors.New("one or more ARMS traces failed to export")

type armsClient interface {
	SearchTracesByPage(ctx context.Context, serviceName string, startMs, endMs int64, pageNumber, pageSize int) (*client.SearchTracesResponse, error)
	GetTrace(ctx context.Context, traceID string) (*client.GetTraceResponse, error)
	GetStack(ctx context.Context, traceID, rpcID string) (*client.GetStackResponse, error)
}

type armsReceiver struct {
	cfg          *Config
	logger       *zap.Logger
	nextConsumer consumer.Traces
	client       armsClient
	state        *poller.State

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

func newReceiver(settings receiver.Settings, cfg *Config, next consumer.Traces) *armsReceiver {
	return &armsReceiver{
		cfg:          cfg,
		logger:       settings.Logger,
		nextConsumer: next,
		state:        poller.NewState(defaultMaxSeenSpans),
	}
}

func (r *armsReceiver) Start(ctx context.Context, _ component.Host) error {
	if r.client == nil {
		r.client = client.New(client.Config{
			Endpoint:        r.cfg.Endpoint,
			Scheme:          r.cfg.Scheme,
			RegionID:        r.cfg.RegionID,
			APIVersion:      r.cfg.APIVersion,
			AccessKeyID:     r.cfg.AccessKeyID,
			AccessKeySecret: string(r.cfg.AccessKeySecret),
			OrganizationID:  r.cfg.OrganizationID,
			ResourceGroupID: r.cfg.ResourceGroupID,
			Timeout:         r.cfg.HTTP.Timeout,
			Logger:          r.logger,
		})
	}

	runCtx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.wg.Add(1)
	go r.loop(runCtx)
	return nil
}

func (r *armsReceiver) Shutdown(context.Context) error {
	if r.cancel != nil {
		r.cancel()
	}
	r.wg.Wait()
	return nil
}

func (r *armsReceiver) loop(ctx context.Context) {
	defer r.wg.Done()

	if r.cfg.InitialDelay > 0 {
		timer := time.NewTimer(r.cfg.InitialDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}

	if err := r.pollOnce(ctx); err != nil {
		r.logger.Error("initial ARMS poll failed", zap.Error(err))
	}

	ticker := time.NewTicker(r.cfg.CollectionInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := r.pollOnce(ctx); err != nil {
				r.logger.Error("ARMS poll failed", zap.Error(err))
			}
		}
	}
}

func (r *armsReceiver) pollOnce(ctx context.Context) error {
	now := time.Now()
	queried := make(map[string]struct{})
	var firstErr error
	for _, service := range r.cfg.Services {
		if err := r.pollService(ctx, service, now, queried); err != nil {
			r.logger.Error("ARMS poll service failed", zap.String("service", service), zap.Error(err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

func (r *armsReceiver) pollService(ctx context.Context, service string, now time.Time, queried map[string]struct{}) (err error) {
	end := now.Add(-r.cfg.QueryDelay)
	start := r.state.Watermark(service)
	initialized := start.IsZero()
	if start.IsZero() {
		start = end.Add(-initialLookback)
		r.state.SetWatermark(service, start)
	}
	queryAttempted := false
	defer func() {
		if entry := r.logger.Check(zap.DebugLevel, "ARMS service poll summary"); entry != nil {
			watermark := r.state.Watermark(service)
			result := "success"
			if err != nil {
				result = "failed"
			} else if !queryAttempted {
				result = "skipped"
			}
			entry.Write(
				zap.String("service", service),
				zap.String("result", result),
				zap.Bool("query_attempted", queryAttempted),
				zap.Int64("start_ms", start.UnixMilli()),
				zap.Int64("end_ms", end.UnixMilli()),
				zap.Bool("watermark_initialized", initialized),
				zap.Time("watermark_before", start),
				zap.Time("watermark_after", watermark),
				zap.Bool("watermark_advanced", watermark.After(start)),
			)
		}
	}()
	if end.Before(start) {
		r.logger.Warn("ARMS query end precedes watermark; waiting for time to advance",
			zap.String("service", service), zap.Time("watermark", start), zap.Time("end", end))
		return nil
	}

	// Catch up in bounded segments instead of skipping the backlog.
	segmented := false
	if end.Sub(start) > maxQueryWindow {
		end = start.Add(maxQueryWindow)
		segmented = true
	}

	startMs := start.UnixMilli()
	endMs := end.UnixMilli()
	queryAttempted = true
	if err := r.pollWindow(ctx, service, startMs, endMs, queried); err != nil {
		return err
	}

	// Advance watermark only after every page and any split windows were consumed.
	wm := end.Add(-r.cfg.OverlapSlack)
	if wm.Before(start) {
		wm = start
	}
	// When catching up in segments, do not apply overlap (would stall progress).
	if segmented {
		wm = end
	}
	r.state.SetWatermark(service, wm)
	return nil
}

func (r *armsReceiver) pollWindow(ctx context.Context, service string, startMs, endMs int64, queried map[string]struct{}) error {
	exportFailed := false
	for page := 1; page <= r.cfg.Page.MaxPage; page++ {
		resp, err := r.client.SearchTracesByPage(ctx, service, startMs, endMs, page, r.cfg.Page.PageSize)
		if err != nil {
			return err
		}
		infos := resp.PageBean.TraceInfos
		if len(infos) == 0 {
			return r.partialExportError(service, exportFailed)
		}

		for _, info := range infos {
			traceID := mapper.StableTraceKey(info.TraceID)
			if traceID == "" {
				exportFailed = true
				r.logger.Warn("ARMS search returned an empty trace ID", zap.String("service", service))
				continue
			}
			if _, ok := queried[traceID]; ok {
				continue
			}
			if err := r.exportTrace(ctx, traceID); err != nil {
				exportFailed = true
				r.logger.Warn("export ARMS trace failed", zap.String("trace_id", traceID), zap.Error(err))
				continue
			}
			queried[traceID] = struct{}{}
		}

		if len(infos) < r.cfg.Page.PageSize {
			return r.partialExportError(service, exportFailed)
		}
		if page == r.cfg.Page.MaxPage {
			// A full final page may be the exact end of the result set. Probe one
			// additional page before deciding whether this time window must split.
			probe, err := r.client.SearchTracesByPage(ctx, service, startMs, endMs, page+1, r.cfg.Page.PageSize)
			if err != nil {
				return err
			}
			if len(probe.PageBean.TraceInfos) == 0 {
				return r.partialExportError(service, exportFailed)
			}
			if endMs-startMs <= 1 {
				return errors.Join(
					r.partialExportError(service, exportFailed),
					fmt.Errorf("ARMS page.max_page exceeded for service %s in an interval of %dms; cannot split further", service, endMs-startMs),
				)
			}

			midMs := startMs + (endMs-startMs)/2
			r.logger.Debug("ARMS search exceeded page.max_page; splitting time window",
				zap.String("service", service),
				zap.Int64("start_ms", startMs),
				zap.Int64("end_ms", endMs),
			)
			leftErr := r.pollWindow(ctx, service, startMs, midMs, queried)
			rightErr := r.pollWindow(ctx, service, midMs, endMs, queried)
			return errors.Join(leftErr, rightErr, r.partialExportError(service, exportFailed))
		}
	}
	return nil
}

func (r *armsReceiver) partialExportError(service string, failed bool) error {
	if failed {
		return fmt.Errorf("%w for service %s", errPartialExport, service)
	}
	return nil
}

func (r *armsReceiver) exportTrace(ctx context.Context, traceID string) error {
	tr, err := r.client.GetTrace(ctx, traceID)
	if err != nil {
		return err
	}
	spans := tr.Spans
	if len(spans) == 0 {
		r.logger.Warn("ARMS GetTrace returned no spans; skipping trace",
			zap.String("trace_id", traceID),
		)
		return nil
	}
	newSpans := make([]client.Span, 0, len(spans))
	keys := make([]poller.SpanKey, 0, len(spans))
	inResponse := make(map[poller.SpanKey]struct{}, len(spans))
	expectedTraceID, _ := mapper.SpanIDs(client.Span{TraceID: traceID})
	for _, span := range spans {
		tid, sid := mapper.SpanIDs(span)
		if tid == (pcommon.TraceID{}) || sid == (pcommon.SpanID{}) {
			return fmt.Errorf("ARMS trace %s contains a span without a valid trace or span ID", traceID)
		}
		if tid != expectedTraceID {
			return fmt.Errorf("ARMS trace %s contains a span with a different trace ID", traceID)
		}
		key := poller.SpanKey{TraceID: tid, SpanID: sid}
		if _, ok := inResponse[key]; ok || r.state.HasSeen(key) {
			continue
		}
		inResponse[key] = struct{}{}
		newSpans = append(newSpans, span)
		keys = append(keys, key)
	}
	if len(newSpans) == 0 {
		return nil
	}

	stacks := map[string][]client.StackFrame{}
	if r.cfg.Stack.Enabled {
		calls := 0
		for _, sp := range newSpans {
			if calls >= r.cfg.Stack.MaxCallsPerTrace {
				break
			}
			if !sp.HaveStack || sp.RpcID == "" {
				continue
			}
			if r.cfg.Stack.OnlyOnError && !mapper.TraceHasError([]client.Span{sp}) {
				continue
			}
			st, err := r.client.GetStack(ctx, traceID, sp.RpcID)
			if err != nil {
				r.logger.Debug("GetStack failed", zap.String("trace_id", traceID), zap.String("rpc_id", sp.RpcID), zap.Error(err))
				continue
			}
			stacks[sp.RpcID] = st.StackInfo
			calls++
		}
	}

	td := mapper.ToTraces(newSpans, stacks, mapper.Options{RegionID: r.cfg.RegionID})
	if err := r.nextConsumer.ConsumeTraces(ctx, td); err != nil {
		return err
	}
	r.state.MarkSeen(keys)
	return nil
}

var _ receiver.Traces = (*armsReceiver)(nil)
