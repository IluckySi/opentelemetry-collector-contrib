# otelarmscol（本地联调精简版）

只包含 ARMS receiver + OTLP/debug exporter，避免全量 `otelcontribcol` 的 Go 版本与依赖问题。

## 编译

```bash
export PATH="$(go env GOPATH)/bin:$PATH"
export GOTOOLCHAIN=local
export GOPROXY=https://goproxy.cn,direct   # 或你们可用的代理

cd /path/to/opentelemetry-collector-contrib

# 首次或改了 builder-config 后：生成源码
builder --config cmd/otelarmscol/builder-config.yaml --skip-compilation

cd cmd/otelarmscol
go mod tidy
CGO_ENABLED=0 go build -trimpath -o ../../bin/otelarmscol_$(go env GOOS)_$(go env GOARCH) .
```

产物：`bin/otelarmscol_darwin_arm64`（macOS arm64）。

## 验证

```bash
./bin/otelarmscol_darwin_arm64 components | rg alibabacloudarms
```
