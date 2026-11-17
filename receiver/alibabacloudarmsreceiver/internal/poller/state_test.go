// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package poller

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHasSeenMarkSeenAndEviction(t *testing.T) {
	s := NewState(2)
	a := SpanKey{SpanID: [8]byte{1}}
	b := SpanKey{SpanID: [8]byte{2}}
	c := SpanKey{SpanID: [8]byte{3}}
	require.False(t, s.HasSeen(a))
	s.MarkSeen([]SpanKey{a})
	require.True(t, s.HasSeen(a))
	s.MarkSeen([]SpanKey{b, c})
	require.False(t, s.HasSeen(a))
	require.True(t, s.HasSeen(b))
	require.True(t, s.HasSeen(c))
}

func TestWatermark(t *testing.T) {
	s := NewState(10)
	require.True(t, s.Watermark("svc").IsZero())
	now := time.Now().UTC().Truncate(time.Second)
	s.SetWatermark("svc", now)
	require.Equal(t, now, s.Watermark("svc"))
}
