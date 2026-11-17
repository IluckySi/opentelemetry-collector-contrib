# Alibaba Cloud ARMS Receiver 开发设计

状态：草案  
范围：Collector Contrib traces receiver，经 **ARMS OpenAPI** 拉专有云调用链 → **OTLP**  
首个环境：国网北京 `bjdc-1`；江苏有同款 OpenAPI 时可复用  
依据：`demo_zw` / `ARMS三步链返回格式.md` / 现场抓包  
相关：`sdd/sgcc-arms-dual-env-access-plan.md`

---

## 1. 目标 / 非目标

**做**

- 组件：`alibabacloudarms`（`receiver/alibabacloudarmsreceiver`），traces only，主动轮询
- 只走 OpenAPI（`Product=ARMS`，`Version=2019-08-08`）
- 按 `services`（serviceName）+ 时间窗拉 Trace → `ptrace`
- 可选 `GetStack`（默认关）

**不做（首版）**

- Console `:8099`、SLS/TLog 直连、Metrics/Logs、告警/拓扑、流式消费

---

## 2. 架构

```text
tick ──► Poller（watermark + Span 内存去重）
           │  per service_name
           ▼
        OpenAPI：SearchTracesByPage → GetTrace →（可选）GetStack
           ▼
        EagleEye → OTLP Mapper ──► ConsumeTraces
```

| 模块 | 职责 |
| --- | --- |
| `client` | POP 签名、Org/RG Header、Action |
| `poller` | 服务×时间窗、watermark、限速、去重 |
| `mapper` | Span（+ 可选 Stack）→ OTLP |

**数据语义（现场已验证）**

| API | 含义 |
| --- | --- |
| `SearchTracesByPage` | Trace 列表摘要 |
| `GetTrace` | 调用链 **Span 节点**（常少量；可能跨服务，也可能单服务切片；**不是**方法全树） |
| `GetStack` | 某 Span（`RpcID`）下的 **方法栈 / SQL / Exception** |

ARMS：粗 Span 搭骨架 + Stack 填细节。映射时 Stack → Span **Events**，不要默认把每帧做成独立 Span。

---

## 3. 配置（精简）

```yaml
receivers:
  alibabacloudarms:
    endpoint: arms-api.res.sgmc.sgcc.com.cn
    region_id: bjdc-1
    scheme: http
    api_version: "2019-08-08"   # POP Version，接口契约；一般勿改

    access_key_id: ${env:ARMS_ACCESS_KEY_ID}
    access_key_secret: ${env:ARMS_ACCESS_KEY_SECRET}
    organization_id: ${env:ARMS_ORG_ID}           # 北京必填
    resource_group_id: ${env:ARMS_RESOURCE_GROUP_ID}

    collection_interval: 30s
    query_delay: 3m
    overlap_slack: 0s

    services:                     # 生产必配；禁止裸查全域
      - px-service-imemberdata
      - xtbg-oa-email-area3

    page:
      page_size: 100
      max_page: 20

    stack:
      enabled: false
      only_on_error: true
      max_calls_per_trace: 3

    http:
      timeout: 30s
```

| 字段 | 说明 |
| --- | --- |
| `api_version` | OpenAPI `Version`（默认 `2019-08-08`），不是数据版本 |
| `services` | 精确 serviceName；空则需后续 discover（P1），生产勿空跑 |
| `stack.*` | GetStack 开关与限流；默认全关 |

P1 再补：`include/exclude` discover、`operation_name`、`min_duration_ms`、`filter.drop_healthy`。

---

## 4. 拉取与去重

```text
每服务每 tick:
  end = now - query_delay；窗口 = [watermark, end]，首次从 end 回溯 5 分钟，单轮不超过 30 分钟
  分页 SearchTracesByPage(ServiceName, …)   # Total 可能封顶 10000，勿当真总数
  对每个 TraceID（单轮跨服务去重）:
      GetTrace → 过滤已导出的 TraceID + SpanID → 可选 GetStack → map → ConsumeTraces
  Consume 成功后再推进 watermark（按 overlap_slack 可选回看，默认 0）
```

**重复 / 丢数**：A→B 且 A、B 都在 `services` 里时，两边列表都会扫到同一 TraceID。  
**去重**：单轮按 TraceID 减少重复详情查询；仅在 `ConsumeTraces` **成功后**按 TraceID + SpanID 记录已导出的 Span。后续轮次重新查到 TraceID 时可补采新 Span；已导出 Span 的更新不重新导出。
**落后追赶**：窗口超过 30 分钟时按段前进，禁止跳窗。
**分页打满**：命中 `page.max_page` 时探测下一页；仍有数据则二分时间窗，无法进一步拆分时不推进 watermark。
内存缓存最多保留 50,000 个 Span，淘汰或重启后可能再导；持久化放 P2。

协议：`HMAC-SHA1` POP；时间毫秒；成功看 `asapiSuccess`；字段 PascalCase（`Key`/`Value`）。

---

## 5. EagleEye → OTLP

| 源 | OTLP |
| --- | --- |
| `TraceID` | `TraceId`（长度不固定；失败则 hash，原文留 `arms.trace_id`） |
| `SpanId` / `ParentSpanId` | 对应字段；空 Parent = 根 |
| `RpcId` | `arms.rpc_id` |
| `ServiceName` | resource `service.name`（按服务拆 ResourceSpans） |
| `OperationName` | span name |
| `Timestamp`/`Duration` ms | 起止时间 |
| `RpcType` | `arms.rpc_type` + SpanKind（0≈SERVER 等） |
| `ResultCode` | `"0"`=Ok，否则 Error |
| `TagEntryList` | attributes（`http.status.code` 可升语义） |
| `GetStack` | Event `arms.stack.frame`（Api/Line/Exception；Type 20→`db.statement`） |

**坑**

1. 只认 OpenAPI：`ResultCode=="0"` 成功（勿混江苏 Console 语义）。
2. 存在 HTTP 200 + 业务失败。
3. `GetTrace` 不保证全链路 / 方法树。
4. `ExtInfo.Type` 可能是 string 或 int。

---

## 6. 包结构与分期

```text
receiver/alibabacloudarmsreceiver/
  factory.go  config.go  receiver.go  README.md  metadata.yaml
  internal/client/   # 签名 + Actions
  internal/poller/   # watermark + dedupe
  internal/mapper/   # EagleEye → OTLP
  internal/testdata/ # 脱敏三步链 JSON
```

| 阶段 | 内容 |
| --- | --- |
| **P0** | client（Search+GetTrace）、services 白名单轮询、基础 mapper、内存 watermark + Span dedupe |
| **P1** | discover + include/exclude、分页限速 |
| **P2** | GetStack、internal metrics、持久 watermark |

稳定性：首 PR `development` → 测试够后 `alpha`。

---

## 7. 风险与验收

| 风险 | 缓解 |
| --- | --- |
| 查询易漏/重复 | watermark + overlap；跨服务 Span 去重；Consume 失败不前进 |
| GetTrace 不完整 | 文档说明；细节开 stack |
| GetStack 打爆网关 | 默认关 + only_on_error + max_calls |
| 全量服务 | 强制白名单 |

验收：配已知 `services` 能在北京持续出 OTLP；无 SLS 依赖；`stack.enabled=false` 零 GetStack；单测不依赖真网。

---

> 一句话：按 serviceName 白名单轮询 `SearchTracesByPage → GetTrace (→ GetStack)`，EagleEye 转 OTLP；默认不拉栈、内存 Span 去重、不依赖 SLS。
