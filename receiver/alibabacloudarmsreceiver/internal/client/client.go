// Copyright The OpenTelemetry Authors
// SPDX-License-Identifier: Apache-2.0

package client // import "github.com/open-telemetry/opentelemetry-collector-contrib/receiver/alibabacloudarmsreceiver/internal/client"

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Config holds OpenAPI client settings.
type Config struct {
	Endpoint        string
	Scheme          string
	RegionID        string
	APIVersion      string
	AccessKeyID     string
	AccessKeySecret string
	OrganizationID  string
	ResourceGroupID string
	Timeout         time.Duration
	HTTPClient      *http.Client
	Logger          *zap.Logger
}

// APIClient calls ARMS OpenAPI Actions over POP RPC.
type APIClient struct {
	cfg        Config
	httpClient *http.Client
}

// New creates an ARMS OpenAPI client.
func New(cfg Config) *APIClient {
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: cfg.Timeout}
	}
	if cfg.Scheme == "" {
		cfg.Scheme = "http"
	}
	if cfg.APIVersion == "" {
		cfg.APIVersion = "2019-08-08"
	}
	if cfg.Logger == nil {
		cfg.Logger = zap.NewNop()
	}
	return &APIClient{cfg: cfg, httpClient: hc}
}

// SearchTracesByPage lists trace summaries for a service in a time window.
func (c *APIClient) SearchTracesByPage(ctx context.Context, serviceName string, startMs, endMs int64, pageNumber, pageSize int) (*SearchTracesResponse, error) {
	params := map[string]string{
		"RegionId":   c.cfg.RegionID,
		"StartTime":  strconv.FormatInt(startMs, 10),
		"EndTime":    strconv.FormatInt(endMs, 10),
		"PageNumber": strconv.Itoa(pageNumber),
		"PageSize":   strconv.Itoa(pageSize),
	}
	if serviceName != "" {
		params["ServiceName"] = serviceName
	}
	var out SearchTracesResponse
	if err := c.call(ctx, "SearchTracesByPage", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetTrace fetches span details for a TraceID.
func (c *APIClient) GetTrace(ctx context.Context, traceID string) (*GetTraceResponse, error) {
	params := map[string]string{
		"RegionId": c.cfg.RegionID,
		"TraceID":  traceID,
	}
	var out GetTraceResponse
	if err := c.call(ctx, "GetTrace", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetStack fetches method-stack frames for a span RpcID.
func (c *APIClient) GetStack(ctx context.Context, traceID, rpcID string) (*GetStackResponse, error) {
	params := map[string]string{
		"RegionId": c.cfg.RegionID,
		"TraceID":  traceID,
		"RpcID":    rpcID,
	}
	var out GetStackResponse
	if err := c.call(ctx, "GetStack", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *APIClient) call(ctx context.Context, action string, biz map[string]string, dest any) error {
	c.cfg.Logger.Debug("ARMS API request",
		zap.String("action", action),
		zap.String("api_version", c.cfg.APIVersion),
		zap.Any("parameters", biz),
	)
	all := map[string]string{
		"Action":            action,
		"AccessKeyId":       c.cfg.AccessKeyID,
		"Format":            "JSON",
		"RegionId":          c.cfg.RegionID,
		"SignatureMethod":   "HMAC-SHA1",
		"SignatureNonce":    strings.ReplaceAll(uuid.NewString(), "-", ""),
		"SignatureVersion":  "1.0",
		"Timestamp":         time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":           c.cfg.APIVersion,
		"Product":           "ARMS",
		"InvokeGatewayType": "POP",
		"apiRegionId":       c.cfg.RegionID,
		"FormatType":        "FORM",
		"isFormat":          "false",
	}
	for k, v := range biz {
		all[k] = v
	}
	all["Signature"] = popSign(c.cfg.AccessKeySecret, all)

	form := url.Values{}
	for k, v := range all {
		form.Set(k, v)
	}
	body := form.Encode()

	endpoint := strings.TrimRight(c.cfg.Endpoint, "/")
	reqURL := fmt.Sprintf("%s://%s/?", c.cfg.Scheme, endpoint)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if c.cfg.OrganizationID != "" {
		req.Header.Set("x-acs-organizationid", c.cfg.OrganizationID)
	}
	if c.cfg.ResourceGroupID != "" {
		req.Header.Set("x-acs-resourcegroupid", c.cfg.ResourceGroupID)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("ARMS %s request failed: %w", action, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("ARMS %s read body: %w", action, err)
	}
	if entry := c.cfg.Logger.Check(zap.DebugLevel, "ARMS API response"); entry != nil {
		entry.Write(zap.String("action", action), zap.Int("http_status", resp.StatusCode), zap.String("body", string(raw)))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ARMS %s HTTP %d: %s", action, resp.StatusCode, truncate(string(raw), 512))
	}
	if err := json.Unmarshal(raw, dest); err != nil {
		return fmt.Errorf("ARMS %s decode: %w", action, err)
	}

	switch v := dest.(type) {
	case *SearchTracesResponse:
		if !v.AsapiSuccess {
			return fmt.Errorf("ARMS %s failed: asapiSuccess=false RequestId=%s Code=%s Message=%s", action, v.RequestID, v.Code, v.Message)
		}
	case *GetTraceResponse:
		if !v.AsapiSuccess {
			return fmt.Errorf("ARMS %s failed: asapiSuccess=false RequestId=%s Code=%s Message=%s", action, v.RequestID, v.Code, v.Message)
		}
	case *GetStackResponse:
		if !v.AsapiSuccess {
			return fmt.Errorf("ARMS %s failed: asapiSuccess=false RequestId=%s Code=%s Message=%s", action, v.RequestID, v.Code, v.Message)
		}
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
