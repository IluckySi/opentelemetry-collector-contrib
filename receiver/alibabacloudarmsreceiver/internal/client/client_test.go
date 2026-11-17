// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package client

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func TestFlexibleStringUnmarshal(t *testing.T) {
	var a ExtInfo
	require.NoError(t, json.Unmarshal([]byte(`{"Type":20,"Info":"sql"}`), &a))
	require.Equal(t, "20", string(a.Type))
	require.Equal(t, "sql", a.Info)

	var b ExtInfo
	require.NoError(t, json.Unmarshal([]byte(`{"Type":"20","Info":"sql"}`), &b))
	require.Equal(t, "20", string(b.Type))
}

func TestGetTraceAgainstFixtureServer(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "get_trace.json"))
	require.NoError(t, err)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "org-test", r.Header.Get("x-acs-organizationid"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		vals, err := url.ParseQuery(string(body))
		require.NoError(t, err)
		require.Equal(t, "GetTrace", vals.Get("Action"))
		require.Equal(t, "ARMS", vals.Get("Product"))
		require.NotEmpty(t, vals.Get("Signature"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(raw)
	}))
	defer srv.Close()

	u, err := url.Parse(srv.URL)
	require.NoError(t, err)

	c := New(Config{
		Endpoint:        u.Host,
		Scheme:          u.Scheme,
		RegionID:        "test-region",
		APIVersion:      "2019-08-08",
		AccessKeyID:     "ak",
		AccessKeySecret: "sk",
		OrganizationID:  "org-test",
		ResourceGroupID: "rs-test",
		HTTPClient:      srv.Client(),
	})
	resp, err := c.GetTrace(t.Context(), "0123456789abcdef0123456789abcdef")
	require.NoError(t, err)
	require.True(t, resp.AsapiSuccess)
	require.Len(t, resp.Spans, 2)
	require.Equal(t, "test-frontend", resp.Spans[0].ServiceName)
}

func TestSearchTracesByPageAgainstServer(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "search_traces.json"))
	require.NoError(t, err)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		vals, _ := url.ParseQuery(string(body))
		require.Equal(t, "SearchTracesByPage", vals.Get("Action"))
		require.Equal(t, "demo-svc", vals.Get("ServiceName"))
		_, _ = w.Write(raw)
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	c := New(Config{
		Endpoint: u.Host, Scheme: u.Scheme, RegionID: "test-region",
		AccessKeyID: "ak", AccessKeySecret: "sk", HTTPClient: srv.Client(),
	})
	resp, err := c.SearchTracesByPage(t.Context(), "demo-svc", 1, 2, 1, 100)
	require.NoError(t, err)
	require.Len(t, resp.PageBean.TraceInfos, 1)
	require.Equal(t, "0123456789abcdef0123456789abcdef", resp.PageBean.TraceInfos[0].TraceID)
}

func TestAPIFailureWithoutCodeIsReturnedAsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"asapiSuccess":false}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c := New(Config{
		Endpoint: u.Host, Scheme: u.Scheme, RegionID: "test-region",
		AccessKeyID: "ak", AccessKeySecret: "sk", HTTPClient: srv.Client(),
	})

	_, err = c.SearchTracesByPage(t.Context(), "demo-svc", 1, 2, 1, 100)
	require.ErrorContains(t, err, "asapiSuccess=false")

	_, err = c.GetTrace(t.Context(), "trace-id")
	require.ErrorContains(t, err, "asapiSuccess=false")

	_, err = c.GetStack(t.Context(), "trace-id", "0")
	require.ErrorContains(t, err, "asapiSuccess=false")
}

func TestAPIDebugLogging(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, r.ParseForm())
		switch r.Form.Get("Action") {
		case "SearchTracesByPage":
			_, _ = w.Write([]byte(`{"asapiSuccess":true,"PageBean":{"TraceInfos":[]}}`))
		case "GetTrace":
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"Code":"50001","Message":"query failed"}`))
		case "GetStack":
			_, _ = w.Write([]byte(`{"asapiSuccess":true,"StackInfo":[]}`))
		default:
			http.Error(w, "unexpected action", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c := New(Config{
		Endpoint: u.Host, Scheme: u.Scheme, RegionID: "test-region",
		AccessKeyID: "private-access-key", AccessKeySecret: "private-secret",
		OrganizationID: "private-organization", ResourceGroupID: "private-resource",
		HTTPClient: srv.Client(), Logger: zap.New(core),
	})

	_, err = c.SearchTracesByPage(t.Context(), "test-service", 100, 200, 1, 50)
	require.NoError(t, err)
	_, err = c.GetTrace(t.Context(), "test-trace")
	require.ErrorContains(t, err, "HTTP 500")
	_, err = c.GetStack(t.Context(), "test-trace", "0.1")
	require.NoError(t, err)

	entries := logs.All()
	require.Len(t, entries, 6)
	for i, action := range []string{"SearchTracesByPage", "GetTrace", "GetStack"} {
		request := entries[i*2]
		response := entries[i*2+1]
		require.Equal(t, "ARMS API request", request.Message)
		require.Equal(t, "ARMS API response", response.Message)
		require.Equal(t, action, request.ContextMap()["action"])
		require.Equal(t, action, response.ContextMap()["action"])
		require.Equal(t, zapcore.DebugLevel, request.Level)
		for _, secret := range []string{"private-access-key", "private-secret", "private-organization", "private-resource"} {
			require.NotContains(t, fmt.Sprint(request.ContextMap()), secret)
		}
	}
	require.Equal(t, map[string]string{
		"RegionId": "test-region", "ServiceName": "test-service", "StartTime": "100",
		"EndTime": "200", "PageNumber": "1", "PageSize": "50",
	}, entries[0].ContextMap()["parameters"])
	require.Equal(t, `{"asapiSuccess":true,"PageBean":{"TraceInfos":[]}}`, entries[1].ContextMap()["body"])
	require.EqualValues(t, 500, entries[3].ContextMap()["http_status"])
	require.Equal(t, `{"Code":"50001","Message":"query failed"}`, entries[3].ContextMap()["body"])
	require.Equal(t, map[string]string{"RegionId": "test-region", "TraceID": "test-trace"}, entries[2].ContextMap()["parameters"])
	require.Equal(t, map[string]string{"RegionId": "test-region", "TraceID": "test-trace", "RpcID": "0.1"}, entries[4].ContextMap()["parameters"])
}

func TestAPIDebugLoggingDisabled(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"asapiSuccess":true}`))
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	require.NoError(t, err)
	c := New(Config{Endpoint: u.Host, Scheme: u.Scheme, HTTPClient: srv.Client(), Logger: zap.New(core)})
	_, err = c.GetTrace(t.Context(), "test-trace")
	require.NoError(t, err)
	require.Zero(t, logs.Len())
}
