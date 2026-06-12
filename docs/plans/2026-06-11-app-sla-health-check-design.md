# 应用 SLA 健康检查设计文档

## 一、项目背景

### 1.1 项目架构

当前网关监测插件由 Go 后端和 React 插件前端组成。后端负责采集 APISIX 网关流量、聚合 Redis 数据并提供 `/api/v1/*` 接口；前端在应用级页面展示流量总览、排行、组件摘要和 SLA 卡片。

现有应用 SLA 接口 `/api/v1/apps/{app_id}/sla` 的计算方式基于请求总数和 5xx 错误数，实际表达的是请求成功率，不是服务在时间维度上的可用性。该语义会误导用户：无流量或低流量场景下，请求成功率无法代表服务是否可用。

### 1.2 现有基础

- 应用页已存在 SLA 卡片展示入口。
- 后端已有 Redis 存储能力、应用 ID 维度的数据聚合能力，以及应用配置读写接口模式。
- 前端已具备应用级上下文，包括 `appID`、`teamName`、`regionName`。
- 当前 SLA 配置只包含目标值，默认目标为 `0.999`，需要调整为健康检查可用性语义。

### 1.3 核心需求

- 用户只需要填写一个健康检查 URL，例如 `https://example.com/healthz`。
- 其他配置由系统固定，不暴露给用户选择：
  - 检查间隔：10 秒
  - 超时时间：3 秒
  - 成功状态码：200-399
  - SLA 目标：99%
  - 数据保留周期：30 天
- SLA 卡片左上角添加齿轮按钮，用于打开配置弹窗。
- 未配置时，SLA 卡片明确提示“未配置健康检查 URL，无法计算应用 SLA”。
- 已配置时，SLA 卡片展示真正基于健康检查采样得到的服务可用性。

## 二、整体架构设计

### 2.1 系统架构图

```text
应用级插件页面
  ├─ SLA 卡片
  │   ├─ 未配置：展示配置提示 + 齿轮入口
  │   └─ 已配置：展示 SLA、目标、检查次数、失败次数、最近状态
  └─ SLA 配置弹窗
      └─ 用户只填写健康检查 URL

插件后端
  ├─ SLA 配置 API
  │   └─ 保存 app_id -> health_check_url
  ├─ 健康检查调度器
  │   └─ 每 10 秒请求已启用应用的 URL
  └─ SLA 查询 API
      └─ 按最近 30 天聚合 bucket 计算可用性

Redis
  ├─ 应用健康检查配置
  ├─ 分钟级 SLA 聚合 bucket
  └─ 最近失败轻量事件
```

### 2.2 核心流程

1. 用户进入应用级网关监测页面。
2. 前端请求 `/api/v1/apps/{app_id}/sla`。
3. 如果后端发现未配置健康检查 URL，返回 `configured=false`。
4. 前端 SLA 卡片展示未配置提示，并提供齿轮配置入口。
5. 用户填写 URL 后，前端调用配置保存接口。
6. 后端保存配置，调度器开始每 10 秒执行健康检查。
7. 每次检查只记录轻量结果，不保存响应体。
8. 查询 SLA 时，后端基于 30 天内的分钟级聚合 bucket 计算：

```text
SLA = 成功检查次数 / 总检查次数
```

## 三、数据模型设计

### 3.1 新增 Redis 数据

#### 健康检查配置

```text
key: nm:app:{app_id}:sla-health-config
type: string(json)
ttl: none
```

结构：

```json
{
  "app_id": "1023",
  "enabled": true,
  "url": "https://example.com/healthz",
  "target": 0.99,
  "interval_seconds": 10,
  "timeout_seconds": 3,
  "success_status_min": 200,
  "success_status_max": 399,
  "updated_at": 1780000000
}
```

用户只填写 `url`。其余字段由后端固定写入，便于前端展示“系统自动配置项”，但不提供修改入口。

#### 分钟级聚合 bucket

```text
key: nm:app:{app_id}:sla-health:bucket:{unix_minute}
type: hash
ttl: 31d
```

字段：

```text
success_count
failure_count
latency_sum_ms
last_status_code
last_error_type
last_checked_at
```

#### 时间索引

```text
key: nm:app:{app_id}:sla-health:index
type: zset
score: unix_minute
member: unix_minute
ttl: 31d
```

查询 30 天窗口时使用 `ZRANGEBYSCORE` 获取 bucket 列表，避免使用 `KEYS`。

#### 最近失败事件

```text
key: nm:app:{app_id}:sla-health:recent-failures
type: list
ttl: 31d
max length: 20
```

只保存轻量原因，不保存响应体：

```json
{
  "checked_at": 1780000000,
  "error_type": "timeout",
  "status_code": 0,
  "latency_ms": 3000
}
```

### 3.2 数据关系

- `app_id` 是 SLA 健康检查的主维度。
- 健康检查配置和 SLA bucket 均按应用隔离。
- SLA 数据不依赖网关请求量，因此无流量时仍然可以计算应用可用性。

## 四、API设计

### 4.1 接口列表

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/apps/{app_id}/sla` | 查询应用 SLA 状态 |
| GET | `/api/v1/apps/{app_id}/sla/config` | 查询应用 SLA 健康检查配置 |
| PUT | `/api/v1/apps/{app_id}/sla/config` | 保存应用 SLA 健康检查 URL |
| DELETE | `/api/v1/apps/{app_id}/sla/config` | 停用应用 SLA 健康检查 |

### 4.2 请求/响应结构

#### 保存配置

请求：

```json
{
  "url": "https://example.com/healthz"
}
```

后端固定补齐：

```json
{
  "target": 0.99,
  "interval_seconds": 10,
  "timeout_seconds": 3,
  "success_status_min": 200,
  "success_status_max": 399
}
```

#### 查询 SLA

未配置：

```json
{
  "data": {
    "app_id": "1023",
    "configured": false,
    "target": 0.99
  },
  "warnings": []
}
```

已配置：

```json
{
  "data": {
    "app_id": "1023",
    "configured": true,
    "current": 0.998,
    "target": 0.99,
    "meeting_target": true,
    "total_checks": 43200,
    "success_checks": 43114,
    "failure_checks": 86,
    "avg_latency_ms": 18.4,
    "last_checked_at": 1780000000,
    "last_status_code": 200,
    "last_error_type": "",
    "window": "30d",
    "interval_seconds": 10,
    "timeout_seconds": 3,
    "success_status_range": "200-399"
  },
  "warnings": []
}
```

为了降低前端改造风险，可以保留现有字段并新增健康检查字段：

```text
current
target
meeting_target
```

继续存在，但语义改为健康检查可用性。

## 五、核心实现设计

### 5.1 关键逻辑

#### URL 校验

- 只允许 `http://` 和 `https://`。
- 禁止空 URL。
- 后端应限制最大长度，例如 2048。
- 不保存响应体，避免泄露敏感信息。

后续如需更严格的 SSRF 防护，可限制内网地址、保留地址或通过集群内代理发起请求；第一阶段至少需要记录清楚请求来源是插件后端。

#### 健康检查判断

```text
成功：HTTP 状态码在 200-399
失败：超时、DNS 失败、连接失败、TLS 错误、状态码不在 200-399
```

失败原因只保存分类：

```text
timeout
dns_error
connection_error
tls_error
status_code_4xx
status_code_5xx
unknown_error
```

#### Redis 写入

每次健康检查写入当前分钟 bucket：

- `HINCRBY success_count 1` 或 `HINCRBY failure_count 1`
- `HINCRBYFLOAT latency_sum_ms {latency}`
- `HSET last_status_code / last_error_type / last_checked_at`
- `ZADD index {unix_minute} {unix_minute}`
- `EXPIRE bucket 31d`
- `EXPIRE index 31d`
- 失败时 `LPUSH recent-failures` 并 `LTRIM 0 19`

写入应使用 pipeline 或 Lua，减少 Redis 往返。

#### 30 天查询

- 固定查询最近 30 天。
- 使用 `ZRANGEBYSCORE` 获取分钟 bucket。
- 聚合成功数、失败数、延迟总和。
- 计算：

```text
total_checks = success_count + failure_count
current = success_count / total_checks
avg_latency_ms = latency_sum_ms / total_checks
```

如果已配置但还没有任何采样点，前端展示“等待首次健康检查结果”。

### 5.2 复用现有代码

- 复用现有应用 SLA 卡片入口。
- 复用现有 `/apps/{app_id}/sla/config` 接口路径，但调整配置语义。
- 复用现有 Redis repository 风格，新增健康检查配置和 bucket 方法。
- 复用前端 `Modal`、`Form`、`Input`、`Tooltip`、`Button` 等 Ant Design 组件。

## 六、实施计划

### Sprint 1: 后端健康检查配置与存储

#### Task 1.1: 扩展 SLA 配置模型

- 文件：`pkg/model/sla.go`
- 实现内容：
  - 新增健康检查配置字段。
  - 新增 SLA 状态字段：`configured`、`total_checks`、`success_checks`、`failure_checks`、`avg_latency_ms`、`last_error_type` 等。
- 验收标准：
  - 旧字段 `current`、`target`、`meeting_target` 保留。
  - JSON 返回兼容现有前端基础字段。

#### Task 1.2: 新增 Redis 存储方法

- 文件：`pkg/repository/redis_store.go`
- 实现内容：
  - 保存/读取/删除健康检查配置。
  - 写入分钟级健康检查 bucket。
  - 查询 30 天窗口健康检查聚合。
  - 维护最近失败事件列表。
- 验收标准：
  - 不使用 `KEYS`。
  - 查询路径使用 `ZRANGEBYSCORE`。
  - 写入使用 pipeline 或 Lua。

#### Task 1.3: 增加后端单元测试

- 文件：`pkg/repository/redis_store_test.go`
- 实现内容：
  - 配置保存/读取测试。
  - bucket 写入和 30 天聚合测试。
  - 最近失败事件只保留 20 条测试。
- 验收标准：
  - `go test ./pkg/repository` 通过。

### Sprint 2: 后端健康检查调度器与 API

#### Task 2.1: 实现健康检查执行器

- 文件：`pkg/service/sla_health_checker.go`
- 实现内容：
  - 按 10 秒间隔请求已启用应用的 URL。
  - 3 秒超时。
  - 200-399 判定成功。
  - 失败原因分类。
- 验收标准：
  - 不保存响应体。
  - 可通过 fake HTTP server 测试成功、500、超时。

#### Task 2.2: 接入插件启动流程

- 文件：`cmd/plugin/main.go`
- 实现内容：
  - 初始化健康检查调度器。
  - 使用现有 Redis store。
  - 插件退出时停止调度器。
- 验收标准：
  - 未配置 URL 时不会发起健康检查。
  - 已配置 URL 时 10 秒内产生采样。

#### Task 2.3: 调整 SLA API

- 文件：`pkg/server/server.go`
- 实现内容：
  - `/apps/{app_id}/sla` 返回健康检查可用性。
  - `/apps/{app_id}/sla/config` 保存 URL，其他字段后端固定。
  - `DELETE /apps/{app_id}/sla/config` 停用健康检查。
- 验收标准：
  - 未配置时返回 `configured=false`。
  - 已配置但无采样时返回明确状态。
  - 已采样时返回 30 天 SLA 聚合结果。

### Sprint 3: 前端应用页 SLA 交互

#### Task 3.1: SLA 卡片状态改造

- 文件：`/Users/guox/Desktop/Project/rainbon-Gateway-monitoring-ui/src/pages/other/index.js`
- 实现内容：
  - 左上角添加齿轮 icon。
  - 未配置时显示配置说明。
  - 已配置时展示核心 SLA 数据。
- 验收标准：
  - 未配置不会显示伪 SLA。
  - 已配置展示 `SLA / 目标 / 检查次数 / 失败次数 / 最近状态`。

#### Task 3.2: SLA 配置弹窗

- 文件：`/Users/guox/Desktop/Project/rainbon-Gateway-monitoring-ui/src/pages/other/index.js`
- 实现内容：
  - 只提供 URL 输入。
  - 弹窗说明固定策略：10 秒检查、3 秒超时、200-399 成功、目标 99%、保留 30 天。
  - 保存后刷新 SLA 卡片。
- 验收标准：
  - 用户无须理解额外参数。
  - URL 为空时前端阻止保存。

#### Task 3.3: API 封装

- 文件：`/Users/guox/Desktop/Project/rainbon-Gateway-monitoring-ui/src/api/index.js`
- 实现内容：
  - 新增/调整获取配置、保存配置、删除配置接口。
- 验收标准：
  - 应用页不直接拼接 fetch。

### Sprint 4: 验证与文档

#### Task 4.1: 后端验证

- 文件：`pkg/service/*_test.go`、`pkg/server/*_test.go`
- 实现内容：
  - 健康检查成功率测试。
  - 未配置状态测试。
  - 配置保存和停用测试。
- 验收标准：
  - `go test ./...` 通过。
  - `go vet ./...` 通过。

#### Task 4.2: 前端构建验证

- 文件：`/Users/guox/Desktop/Project/rainbon-Gateway-monitoring-ui`
- 实现内容：
  - 执行 `npm run build`。
  - 同步 `dist/main.js` 到插件后端静态文件。
- 验收标准：
  - 构建通过。
  - 插件应用页能展示配置和已配置两种状态。

## 七、关键参考代码

| 功能 | 文件 | 说明 |
|------|------|------|
| 当前 SLA 服务 | `pkg/service/sla.go` | 现有请求成功率计算位置，需要替换为健康检查可用性 |
| SLA 模型 | `pkg/model/sla.go` | 扩展配置和返回字段 |
| SLA API | `pkg/server/server.go` | `/apps/{app_id}/sla` 和 `/sla/config` 路由 |
| Redis 存储 | `pkg/repository/redis_store.go` | 新增健康检查配置和 bucket 存储 |
| 应用页 SLA 卡片 | `/Users/guox/Desktop/Project/rainbon-Gateway-monitoring-ui/src/pages/other/index.js` | 前端展示和配置入口 |
| 应用页样式 | `/Users/guox/Desktop/Project/rainbon-Gateway-monitoring-ui/src/pages/other/index.less` | SLA 卡片布局和配置提示样式 |
| 前端 API | `/Users/guox/Desktop/Project/rainbon-Gateway-monitoring-ui/src/api/index.js` | 封装 SLA 配置接口 |

