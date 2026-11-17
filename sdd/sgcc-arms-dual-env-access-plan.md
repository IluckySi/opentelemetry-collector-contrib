# 国网双环境 ARMS Trace 接入方案

状态：草案  
依据：

- `RCA-demo` 已联通江苏 / 北京国网的现有调用
- 北京国网现场抓包：`SearchTracesByPage` → `GetTrace` → `GetStack`
- 两地均为专有云 ARMS 3.x，Trace **不落 SLS**，明细在 TLog/ESDB，对外只走 ARMS API

目标：为 Collector `alibabacloud_arms` receiver（以及后续统一 RCA 工具层）提供一套可同时覆盖江苏、北京的取数方案。

---

## 1. 结论

| 问题 | 结论 |
| --- | --- |
| Trace 能否从 SLS 拉？ | **不能**。两地专有云均无 `proj-xtrace-*` / `logstore-tracing` 可用面 |
| 是否只能走 ARMS API？ | **是**。正式接入面只有 ARMS OpenAPI / Console POP |
| 数据模型是不是 OTLP 3.x 信封？ | **不是**。两地返回的是 **EagleEye 调用链模型**（`TraceID`/`RpcId`/`TagEntryList`，时间毫秒） |
| 北京现场验证链路？ | `SearchTracesByPage` → `GetTrace` →（`HaveStack=true` 时）`GetStack` |
| 江苏 Demo 当前链路？ | Console form：`/trace/Services.json`、`/trace/Traces.json`、`/trace/Trace.json` 等 |
| 推荐 receiver 主路径？ | **以北京已验证的 OpenAPI Action 为主路径**；江苏若 Console 与 OpenAPI 并存，优先对齐 OpenAPI，Console 作兼容适配器 |

---

## 2. 两套已验证入口对比

### 2.1 江苏（RCA-demo 现行）

- 入口：`ARMS_HOST`，默认  
  `http://arms.console.pop.js-1.res.sgmc.sgcc.com.cn:8099`
- 协议：HTTP `application/x-www-form-urlencoded` POST
- 鉴权：`_userId` + `accessKeyId`（Demo 侧不传 Secret 到 Console form）
- Region：`js-1`
- 路径与用途：

| Demo 工具 | Path | 作用 |
| --- | --- | --- |
| `arms_get_services` | `/trace/Services.json` | 应用清单 → 解析 `pid` |
| `arms_get_dependencies` | `/trace/Dependecies.json` | 拓扑 |
| `arms_search_traces` | `/trace/Traces.json` | Trace 列表摘要 |
| `arms_get_trace_detail` | `/trace/Trace.json` | 单 Trace Span 明细 |
| `arms_list_alert_events` | `/alert/AlertEvents.json` | 告警事件 |
| `arms_get_metrics` | `/metric/Metric.json` | 指标（graph 暂未启用） |

- Trace 列表入参要点：`appId`、`serviceName`、`operationName?`、`startTime`/`endTime`（毫秒）、`isError`、`limit`、`regionId`
- Trace 明细响应形状（fixture）：`{ code, success, data: [ spans... ] }`，span 字段为 camelCase（`traceID`、`rpcId`、`tagEntryList`、`haveStack`）

### 2.2 北京（现场测试）

- 入口：`arms-api.res.sgmc.sgcc.com.cn`
- 协议：POP Gateway，`Product=ARMS`，`Version=2019-08-08`，`HttpMethod=POST`，`FormatType=FORM`，`InvokeGatewayType=POP`
- 鉴权：`AccessKeyId` + `AccessKeySecret`
- 必带 Header：
  - `x-acs-organizationid`
  - `x-acs-resourcegroupid`
- Region：`bjdc-1`
- 已跑通 Action：

| 顺序 | Action | 作用 | 关键入参 | 关键出参 |
| --- | --- | --- | --- | --- |
| 1 | `SearchTracesByPage` | Trace 列表 | `RegionId`、`StartTime`、`EndTime`（毫秒）；可选 `ServiceName` 等 | `PageBean.TraceInfos[]`（`TraceID`/`ServiceName`/`OperationName`/`Duration`/`Timestamp`） |
| 2 | `GetTrace` | Span 明细 | `RegionId`、`TraceID` | `Spans[]` |
| 3 | `GetStack` | 方法栈 / SQL 细节 | `RegionId`、`TraceID`、`RpcID` | `StackInfo[]` |

- 现场样例要点：
  - `SearchTracesByPage` 不带 `ServiceName` 时跨应用返回，`Total` 可达上万，必须按应用/时间窗收口
  - `GetTrace` 的 Span 与江苏 Console 明细 **同一套 EagleEye 语义**；字段多为 PascalCase
  - `HaveStack=true` 时可再调 `GetStack`，拿到方法调用、行号、SQL（`ExtInfo.Type=20`）等，这是 Console Demo 尚未接入的增强信息

### 2.3 差异摘要

| 维度 | 江苏 Demo（Console） | 北京现场（OpenAPI） |
| --- | --- | --- |
| Endpoint 形态 | `arms.console.pop.<region>...:8099` + JSON path | `arms-api...` + `Action=` |
| 鉴权 | `_userId` + AK（无 SK） | AK + SK + Org/RG Header |
| 列表 Action/Path | `/trace/Traces.json` | `SearchTracesByPage` |
| 明细 | `/trace/Trace.json` | `GetTrace` |
| 栈信息 | 未用（fixture 多为 `haveStack=false`） | `GetStack` 已验证 |
| Region | `js-1` | `bjdc-1` |
| Span 语义 | EagleEye | EagleEye（一致） |
| 时间单位 | 毫秒 | 毫秒 |

**关键判断：两地 Trace 业务模型一致，差异主要在「网关/鉴权/Action 名/字段大小写」，不是两套存储模型。**

---

## 3. 推荐架构

```text
                    ┌─────────────────────────┐
                    │  alibabacloud_arms      │
                    │  receiver (Collector)   │
                    └───────────┬─────────────┘
                                │
                     ┌──────────▼──────────┐
                     │  ArmsAccessFacade   │  统一：ListApps / SearchTraces / GetTrace / GetStack?
                     └──────────┬──────────┘
                ┌───────────────┴───────────────┐
                ▼                               ▼
     ┌────────────────────┐          ┌────────────────────┐
     │ OpenAPIAdapter     │          │ ConsoleAdapter     │
     │ (北京主路径；江苏优先│          │ (江苏现网兜底)      │
     │  对齐到此)          │          │ Services/Traces/   │
     │ SearchTracesByPage │          │ Trace.json ...     │
     │ GetTrace / GetStack│          └────────────────────┘
     └────────────────────┘
                                │
                     ┌──────────▼──────────┐
                     │ EagleEye → OTLP     │
                     │ Span Mapper         │
                     └────────────────────┘
```

原则：

1. **存储无关**：不做 SLS / TLog / ESDB 直连。
2. **双适配器，单映射器**：Adapter 只负责协议与鉴权；Span → OTLP 只维护一份。
3. **北京路径作为规范主路径**：现场已证明 `SearchTracesByPage` + `GetTrace` + `GetStack` 可用。
4. **江苏保留 ConsoleAdapter**：保证 RCA-demo 现网不中断；同步在江苏验证同名 OpenAPI，能通则切主路径。

---

## 4. 统一能力与接口映射

### 4.1 Facade 能力

| Facade 方法 | OpenAPI | Console（江苏现状） | Receiver 是否必需 |
| --- | --- | --- | --- |
| `ListServices` | `SearchTraceAppByPage`（待两边复核） | `/trace/Services.json` | 建议有（按应用轮询） |
| `SearchTraces` | `SearchTracesByPage` | `/trace/Traces.json` | **必需** |
| `GetTrace` | `GetTrace` | `/trace/Trace.json` | **必需** |
| `GetStack` | `GetStack` | 待确认 Console 是否有对等 path | 可选（首版可配置开关） |
| `GetDependencies` | 待确认 | `/trace/Dependecies.json` | RCA 需要；receiver 首版可不做 |
| `ListAlertEvents` | 待确认 | `/alert/AlertEvents.json` | RCA 需要；receiver 不做 |

### 4.2 Receiver 拉取流程（首版）

```text
for each configured service (或 ListServices 全量，受 allowlist 限制):
  page = 1
  loop SearchTraces(service, start, end, page):
    collect TraceIDs (dedupe)
    for batch in TraceIDs (建议 1 条/次先稳，后续再评估批量):
      spans = GetTrace(traceID)
      optional: for span in spans where HaveStack:
                  stack = GetStack(traceID, rpcId)  # 写入 span events / attributes
      map EagleEye spans → ptrace.Traces
      export to pipeline
  advance watermark = max(seen Timestamp)
```

约束（来自现场）：

- `SearchTracesByPage` 必须带 `ServiceName`（或等价过滤），禁止裸查全域。
- 时间窗用毫秒；滑动窗口 + watermark，避免重复拉取。
- `PageSize` 建议 ≤ 100；`Total` 很大时按页消费并限速。
- 北京样例里 `GetTrace` 可能只返回部分 Span（单服务视角）；映射时按返回如实转换，不要假设完整跨服务树一定在一次响应里。

---

## 5. EagleEye → OTLP 映射（两地共用）

| EagleEye 字段 | OTLP |
| --- | --- |
| `TraceID` / `traceID` | `TraceId`：按十六进制解析；长度不足则左补零到 16 字节；原文保留 `arms.trace_id` |
| `SpanId` / `spanId` | `SpanId` |
| `ParentSpanId` / `parentSpanId` | `ParentSpanId`；空/哨兵值视为根 |
| `RpcId` / `rpcId` | attribute `arms.rpc_id`（父子关系优先用 ParentSpanId，RpcId 作补充） |
| `ServiceName` | resource `service.name` |
| `ServiceIp` | `net.host.ip` / `arms.service_ip` |
| `OperationName` | `Name` |
| `Timestamp`（ms） | `StartTimeUnixNano = ms * 1e6` |
| `Duration`（ms） | `EndTime = Start + duration*1e6` |
| `RpcType` | `arms.rpc_type`；并推导 `SpanKind`（0≈SERVER HTTP，消息收发作 CONSUMER/PRODUCER 等，表驱动） |
| `ResultCode` | `Status`（非 `"0"` → Error） |
| `TagEntryList[].Key/Value` | attributes（`http.status.code` 等原样或标准语义提升） |
| `HaveStack` + `GetStack.StackInfo` | 可选：`Events` 或 `arms.stack.*` attributes（`Api`/`Line`/`ExtInfo.Info` SQL） |
| `LogEventList` | Events（若非空） |

说明：这不是公有云 ARMS Agent 3.x 的 OTLP/SLS 信封；专有云 3.x 对外 API 仍是 EagleEye。文档与代码中应称为 **`arms_eagleeye` / `private_cloud_v3` 解码器**，避免和公有云 SLS `logstore-tracing` 3.x 解码器混淆。

---

## 6. 多环境配置模型

```yaml
receivers:
  alibabacloud_arms:
    mode: openapi            # openapi | console
    endpoint: arms-api.res.sgmc.sgcc.com.cn
    region_id: bjdc-1
    access_key_id: ${env:ARMS_AK}
    access_key_secret: ${env:ARMS_SK}
    organization_id: org-xxx          # 北京必填
    resource_group_id: rs-xxx         # 北京必填
    http_protocol: HTTPS              # 以现场为准
    poll_interval: 30s
    lookback: 5m
    page_size: 100
    services:                         # 强烈建议配置 allowlist
      - px-service-imemberdata
    include_stack: false              # GetStack 开关；默认关，控制放大
    # console 兼容（江苏）：
    # mode: console
    # endpoint: http://arms.console.pop.js-1.res.sgmc.sgcc.com.cn:8099
    # user_id: ${env:ARMS_USER_ID}
    # access_key_id: ${env:ARMS_AK}
    # region_id: js-1
```

部署建议：

- **一环境一 receiver 实例/一套配置**（北京一套、江苏一套），不要在同一进程里混 Region 凭证。
- 凭证只走环境变量 / Secret，不入库、不进方案样例明文。
- RCA-demo 可逐步把 `ArmsClient` 拆成同一套 `openapi|console` 双适配，与 Collector 对齐，减少两套协议漂移。

---

## 7. 与 RCA-demo 的关系

| 能力 | RCA-demo 现状 | 本方案对 Demo 的建议 |
| --- | --- | --- |
| Trace 列表/明细 | Console path，已通江苏（及北京连通声明） | 增加 OpenAPI 后端；按 `ARMS_MODE` 切换 |
| GetStack | 未接入 | 北京可先做可选工具 `arms_get_stack`，有 `HaveStack` 再调 |
| 告警/拓扑/指标 | Console | 继续走 Console，直到 OpenAPI 对等接口在两地验证 |
| SLS 日志 | 江苏有独立 SLS 查询 | 与 Trace 解耦；北京若无对等 SLS，RCA 日志支路按环境降级 |

Collector receiver **只负责 Trace 持续拉取 → OTLP**；告警驱动的 RCA 仍由 Demo/Agent 完成，二者共用映射与适配器设计即可。

---

## 8. 实施分期

### P0（必须）

1. OpenAPI 客户端：签名/POST、Org/RG Header、`SearchTracesByPage`、`GetTrace`
2. EagleEye → OTLP 映射 + 单测（用北京脱敏样例 + 江苏 fixture）
3. 按 `ServiceName` + 时间窗轮询、去重、watermark
4. 配置分离：`bjdc-1` / `js-1`

### P1

1. 江苏侧验证同一套 OpenAPI 是否可用；可用则 Console 降为 fallback
2. `GetStack` 可选接入（`include_stack`）
3. `ListServices` / allowlist 自动发现
4. 限速、退避、分页完整性（`Total` vs 已拉条数）

### P2

1. RCA-demo `ArmsClient` 与 Collector 对齐双适配
2. 拓扑/告警 OpenAPI 化（仅 RCA 需要）
3. 监控指标：拉取延迟、空窗、API 错误率、重复 Trace 比例

---

## 9. 待现场确认清单

1. 江苏是否开放与北京相同的 `arms-api` OpenAPI（含 `SearchTracesByPage`/`GetTrace`/`GetStack`）？
2. 北京 Console POP（`:8099` + `/trace/*.json`）是否仍可用，还是仅 OpenAPI？
3. `GetTrace` 在跨服务调用时是否返回全链路 Span，还是单应用切片？
4. `SearchTracesByPage` 过滤字段全集（`ServiceName`/`OperationName`/`MinDuration`/`IsError` 等）在两地的支持差异。
5. 北京告警、拓扑、指标是否也有 OpenAPI；若无，RCA 在北京是否继续依赖 Console。
6. 组织 ID / 资源组 ID 是否按环境固定，能否只读子账号权限最小化。

---

## 10. 风险与边界

- **不是流式消费**：API 是查询模型，只能轮询；会有延迟与重复，需 watermark + dedupe。
- **GetStack 放大**：每 Span 额外请求，默认关闭。
- **无 SLS 时 RCA 日志支路**：北京若无业务日志 SLS，不能假设与江苏相同的 `sls_search_logs` 路径。
- **凭证安全**：现场抓包文件含明文 AK；方案与仓库不得收录；应轮换泄露凭证。
- **勿直连 TLog/ESDB**：无正式协议、权限与稳定性保障，不作为交付路径。

---

## 11. 一句话交付口径

> 国网江苏/北京 ARMS 3.x 专有云 Trace 不进 SLS；以 ARMS API 拉取 EagleEye 调用链并转 OTLP。北京已验证 `SearchTracesByPage → GetTrace → GetStack`；江苏 Demo 现用 Console `/trace/*.json`。实现上采用 OpenAPI 主适配 + Console 兼容适配、共用 EagleEye→OTLP 映射，按 Region 分套配置部署。
