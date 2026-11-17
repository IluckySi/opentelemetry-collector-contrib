// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package client // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/client"

import "encoding/json"

// SearchTracesResponse is the SearchTracesByPage response body.
type SearchTracesResponse struct {
	AsapiSuccess bool      `json:"asapiSuccess"`
	RequestID    string    `json:"RequestId"`
	Code         string    `json:"Code"`
	Message      string    `json:"Message"`
	PageBean     PageBean  `json:"PageBean"`
}

// PageBean holds paginated trace summaries.
type PageBean struct {
	PageSize    int         `json:"PageSize"`
	PageNumber  int         `json:"PageNumber"`
	Total       int         `json:"Total"`
	TraceInfos  []TraceInfo `json:"TraceInfos"`
}

// TraceInfo is a list-row summary from SearchTracesByPage.
type TraceInfo struct {
	TraceID       string `json:"TraceID"`
	ServiceName   string `json:"ServiceName"`
	OperationName string `json:"OperationName"`
	ServiceIP     string `json:"ServiceIp"`
	Duration      int64  `json:"Duration"`
	Timestamp     int64  `json:"Timestamp"`
}

// GetTraceResponse is the GetTrace response body.
type GetTraceResponse struct {
	AsapiSuccess bool   `json:"asapiSuccess"`
	RequestID    string `json:"RequestId"`
	Code         string `json:"Code"`
	Message      string `json:"Message"`
	Spans        []Span `json:"Spans"`
}

// Span is an EagleEye call-chain node from GetTrace.
type Span struct {
	TraceID       string     `json:"TraceID"`
	SpanID        string     `json:"SpanId"`
	ParentSpanID  string     `json:"ParentSpanId"`
	ServiceName   string     `json:"ServiceName"`
	ServiceIP     string     `json:"ServiceIp"`
	OperationName string     `json:"OperationName"`
	RpcID         string     `json:"RpcId"`
	RpcType       int        `json:"RpcType"`
	ResultCode    string     `json:"ResultCode"`
	Duration      int64      `json:"Duration"`
	Timestamp     int64      `json:"Timestamp"`
	HaveStack     bool       `json:"HaveStack"`
	TagEntryList  []TagEntry `json:"TagEntryList"`
	LogEventList  []LogEvent `json:"LogEventList"`
}

// TagEntry is a Key/Value attribute on a span.
type TagEntry struct {
	Key   string `json:"Key"`
	Value string `json:"Value"`
}

// LogEvent is an EagleEye log event attached to a span.
type LogEvent struct {
	Timestamp    int64      `json:"Timestamp"`
	TagEntryList []TagEntry `json:"TagEntryList"`
}

// GetStackResponse is the GetStack response body.
type GetStackResponse struct {
	AsapiSuccess bool        `json:"asapiSuccess"`
	RequestID    string      `json:"RequestId"`
	Code         string      `json:"Code"`
	Message      string      `json:"Message"`
	StackInfo    []StackFrame `json:"StackInfo"`
}

// StackFrame is one method-stack / SQL frame under a span.
type StackFrame struct {
	RpcID       string  `json:"RpcId"`
	Api         string  `json:"Api"`
	Line        string  `json:"Line"`
	ServiceName string  `json:"ServiceName"`
	StartTime   int64   `json:"StartTime"`
	Duration    int64   `json:"Duration"`
	Exception   string  `json:"Exception"`
	ExtInfo     ExtInfo `json:"ExtInfo"`
}

// ExtInfo carries typed stack extras (e.g. Type 20 = SQL).
// Type may be string or number in the wire format.
type ExtInfo struct {
	Type FlexibleString `json:"Type"`
	Info string         `json:"Info"`
}

// FlexibleString unmarshals JSON string or number into a string.
type FlexibleString string

func (f *FlexibleString) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		*f = ""
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = FlexibleString(s)
		return nil
	}
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		*f = FlexibleString(n.String())
		return nil
	}
	*f = FlexibleString(string(b))
	return nil
}
