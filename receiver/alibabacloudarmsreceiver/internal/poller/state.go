// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package poller // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/poller"

import (
	"sync"
	"time"

	"go.opentelemetry.io/collector/pdata/pcommon"
)

// SpanKey identifies a span using the IDs exported to OTLP.
type SpanKey struct {
	TraceID pcommon.TraceID
	SpanID  pcommon.SpanID
}

// State keeps per-service next-query cursors and an in-memory span dedupe set.
type State struct {
	mu         sync.Mutex
	watermarks map[string]time.Time
	seen       map[SpanKey]struct{}
	seenOrder  []SpanKey
	maxSeen    int
}

// NewState creates poller state with a bounded span cache.
func NewState(maxSeen int) *State {
	if maxSeen <= 0 {
		maxSeen = 10000
	}
	return &State{
		watermarks: make(map[string]time.Time),
		seen:       make(map[SpanKey]struct{}),
		maxSeen:    maxSeen,
	}
}

// Watermark returns the next query start time for a service (zero if unset).
func (s *State) Watermark(service string) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.watermarks[service]
}

// SetWatermark records the initial or next query start time for a service.
func (s *State) SetWatermark(service string, t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watermarks[service] = t
}

// HasSeen reports whether the span was already successfully exported.
func (s *State) HasSeen(key SpanKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[key]
	return ok
}

// MarkSeen records successfully exported spans (FIFO eviction when full).
func (s *State) MarkSeen(keys []SpanKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, key := range keys {
		if _, ok := s.seen[key]; ok {
			continue
		}
		s.seen[key] = struct{}{}
		s.seenOrder = append(s.seenOrder, key)
		for len(s.seenOrder) > s.maxSeen {
			old := s.seenOrder[0]
			s.seenOrder = s.seenOrder[1:]
			delete(s.seen, old)
		}
	}
}
