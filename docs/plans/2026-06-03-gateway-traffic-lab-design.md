# Gateway Traffic Lab 设计文档

## 一、项目背景

### 1.1 项目架构

当前 `rainbond-plugin-template` 已实现 Rainbond 网关监控插件，核心链路为 APISIX `http-logger` 将网关访问日志写入插件后端 `/api/v1/collector/apisix/logs`，后端按 route、team、app、component 和内部路由分组聚合到 Redis，再由前端查询展示请求量、错误率、延迟 TopN、SLA 等指标。

本设计新增一个独立的测试业务应用 `gateway-traffic-lab`。它不直接写入监控插件，也不伪造 Redis 数据，而是通过 Rainbond 网关暴露前端页面和后端测试接口。用户在页面点击按钮后，浏览器向测试应用自身的后端发送真实 HTTP 请求，APISIX 记录真实 access log，监控插件按现有采集链路产生监控数据。

### 1.2 现有基础

`rainbond-plugin-template` 中已有以下能力可复用或对齐：

- `pkg/server`：插件业务 API 和 collector 路由结构。
- `pkg/model/collector.go`：APISIX access log 字段包括 `route_id`、`service_id`、`uri`、`status`、`request_time`、`upstream_status`。
- `pkg/service/collector.go`：按 URI 分组并聚合请求数、5xx 错误数、上游 5xx 错误数、平均延迟。
- 默认路由分组示例：`/api/user/setting/*`、`/api/order/detail/*`。
- README 中已约定 Route 级 `http-logger` 挂载和应用级同步接口。

测试应用需要主动覆盖这些监控维度，尤其是不同 URI、不同状态码、不同响应耗时、批量请求和错误率。

### 1.3 核心需求

- 提供一个可部署到 Rainbond 的独立前后端简易应用。
- 页面点击即可发送不同类型请求，让网关监控插件产生可见数据。
- 支持控制响应时间、请求次数、并发数、错误率、HTTP 方法、状态码和内部路由。
- 支持一键场景，例如正常流量、慢请求流量、错误流量、混合流量。
- 后端返回结构化响应，前端展示每次请求结果、耗时和汇总。
- 应用应轻量、无数据库依赖、单容器即可运行。

## 二、整体架构设计

### 2.1 系统架构图

```text
Browser
  -> Rainbond Gateway / APISIX
     -> gateway-traffic-lab frontend
        -> Rainbond Gateway / APISIX
           -> gateway-traffic-lab backend /api/*
              -> response with controlled status, latency, body

APISIX http-logger
  -> rainbond-gateway-monitoring /api/v1/collector/apisix/logs
     -> Redis hot window aggregation
        -> monitoring plugin UI
```

### 2.2 核心流程

1. 用户在 Rainbond 中部署 `gateway-traffic-lab`，为服务配置网关访问地址。
2. 用户在网关监控插件页面对该应用执行 Route 级 `http-logger` 同步。
3. 用户访问 `gateway-traffic-lab` 前端页面。
4. 用户选择测试场景或手动配置参数。
5. 前端按配置向自身后端发送真实 HTTP 请求。
6. 后端根据请求参数 sleep、返回指定状态码或随机错误。
7. APISIX 记录访问日志，`http-logger` 推送到监控插件 collector。
8. 监控插件聚合后，应用内部路由、错误 TopN、延迟 TopN 和 SLA 页面出现数据。

## 三、数据模型设计

### 3.1 新增数据库表

无需新增数据库表。测试应用所有配置在前端状态中维护，后端为无状态服务。

后端请求参数模型：

```go
type TrafficRequest struct {
    DelayMS       int    `json:"delay_ms"`
    Status        int    `json:"status"`
    ErrorRate     int    `json:"error_rate"`
    MinDelayMS    int    `json:"min_delay_ms"`
    MaxDelayMS    int    `json:"max_delay_ms"`
    PayloadBytes  int    `json:"payload_bytes"`
    RouteLabel    string `json:"route_label"`
}
```

前端批量任务模型：

```ts
type TrafficScenario = {
  name: string
  method: 'GET' | 'POST' | 'PUT' | 'DELETE'
  path: string
  count: number
  concurrency: number
  delayMs: number
  minDelayMs: number
  maxDelayMs: number
  status: number
  errorRate: number
}
```

### 3.2 数据关系

测试应用不持久化业务数据。请求与监控数据的关系由网关和监控插件维护：

- 请求 path 映射为 APISIX log 的 `uri`。
- 响应状态码映射为 `status` 和 `upstream_status`。
- 后端 sleep 时间映射为 `request_time` 和上游响应耗时。
- Rainbond Route 映射提供 app、component、service alias 等聚合维度。

## 四、API设计

### 4.1 接口列表

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/healthz` | 健康检查 |
| GET | `/api/ping` | 快速 200 请求 |
| GET | `/api/delay` | 固定延迟请求，参数 `ms` |
| GET | `/api/error` | 指定错误状态码，参数 `status`、`ms` |
| GET | `/api/random` | 随机延迟和随机错误，参数 `errorRate`、`minMs`、`maxMs` |
| GET/POST | `/api/user/setting/{id}` | 用户设置路由分组测试 |
| GET/POST | `/api/order/detail/{id}` | 订单详情路由分组测试 |
| GET/POST | `/api/report/list` | 报表列表路由测试 |
| GET/POST/PUT/DELETE | `/api/echo` | 方法、请求体和 payload 测试 |
| POST | `/api/scenario` | 单次场景执行，body 控制状态码、延迟、payload |

批量请求默认由前端在浏览器端执行，便于每一次请求都经过网关并被 access log 记录。后端不提供“内部循环批量请求”作为主路径，避免只产生一次网关请求而无法反映真实请求次数。

### 4.2 请求/响应结构

`GET /api/delay?ms=1200`

```json
{
  "ok": true,
  "route": "/api/delay",
  "status": 200,
  "delay_ms": 1200,
  "timestamp": "2026-06-03T15:00:00+08:00"
}
```

`GET /api/random?errorRate=30&minMs=100&maxMs=1500`

```json
{
  "ok": false,
  "route": "/api/random",
  "status": 500,
  "delay_ms": 841,
  "error_rate": 30,
  "timestamp": "2026-06-03T15:00:00+08:00"
}
```

错误状态码由后端真实写入 HTTP response status，前端根据 `response.status` 记录成功或失败。

## 五、核心实现设计

### 5.1 关键逻辑

后端使用 Go 标准库 `net/http` 实现，避免引入数据库和复杂框架。核心函数包括：

- `parseIntRange`：解析并限制 `ms`、`count`、`payloadBytes` 等参数。
- `sleepDelay`：执行固定或随机延迟，最大默认限制为 30 秒。
- `resolveStatus`：限制可返回状态码范围为 200-599，默认 200。
- `writeTrafficResponse`：按指定状态码写入 JSON。
- `handleRandom`：根据 `errorRate` 生成 200 或 500，按 `minMs/maxMs` 生成随机延迟。
- `handleGroupedRoute`：处理 `/api/user/setting/{id}`、`/api/order/detail/{id}` 等内部路由分组。

前端使用 React 实现一个操作台，页面包含：

- 场景快捷按钮：正常流量、慢请求、错误流量、混合流量。
- 参数面板：路径、方法、次数、并发、延迟、状态码、错误率。
- 执行状态：进度、成功数、失败数、平均耗时、最近响应列表。
- 路由按钮组：快速切换到 `/api/user/setting/{id}`、`/api/order/detail/{id}`、`/api/report/list`。

前端批量执行使用并发池控制，不一次性创建过多请求。默认上限：

- `count <= 1000`
- `concurrency <= 50`
- `delayMs <= 30000`
- `payloadBytes <= 1048576`

### 5.2 复用现有代码

本测试应用不直接复用插件后端包，以免把业务测试服务和授权/collector 插件耦合。实现时可以参考当前仓库的 Go 项目规范、Makefile 验证方式和 Dockerfile 多阶段构建方式。

建议新增目录：

```text
examples/gateway-traffic-lab/
├── backend/
│   ├── cmd/server/main.go
│   ├── internal/traffic/handler.go
│   ├── internal/traffic/handler_test.go
│   ├── go.mod
│   └── go.sum
├── frontend/
│   ├── package.json
│   ├── index.html
│   ├── src/App.jsx
│   ├── src/main.jsx
│   └── src/styles.css
├── Dockerfile
├── rainbond.yaml
└── README.md
```

## 六、实施计划

### Sprint 1: 后端流量控制服务

#### Task 1.1: 创建 Go 后端骨架
- 文件：`examples/gateway-traffic-lab/backend/go.mod:1`
- 实现内容：初始化 Go module，创建 `cmd/server/main.go`，监听 `PORT` 环境变量，默认 `8080`。
- 验收标准：`go test ./...` 和 `go build ./...` 通过。

#### Task 1.2: 实现基础测试接口
- 文件：`examples/gateway-traffic-lab/backend/internal/traffic/handler.go:1`
- 实现内容：实现 `/healthz`、`/api/ping`、`/api/delay`、`/api/error`、`/api/random`。
- 验收标准：单元测试覆盖状态码、延迟参数、随机错误率边界。

#### Task 1.3: 实现内部路由分组接口
- 文件：`examples/gateway-traffic-lab/backend/internal/traffic/handler.go:1`
- 实现内容：实现 `/api/user/setting/{id}`、`/api/order/detail/{id}`、`/api/report/list`、`/api/echo`、`/api/scenario`。
- 验收标准：不同 path 能返回自身 route 信息，错误状态码真实写入 response。

### Sprint 2: 前端点击式流量操作台

#### Task 2.1: 创建 React/Vite 前端骨架
- 文件：`examples/gateway-traffic-lab/frontend/package.json:1`
- 实现内容：创建 React 页面入口和构建脚本。
- 验收标准：`npm run build` 生成静态产物。

#### Task 2.2: 实现场景按钮和参数面板
- 文件：`examples/gateway-traffic-lab/frontend/src/App.jsx:1`
- 实现内容：实现快捷场景、参数输入、路径选择、方法选择、启动/停止按钮。
- 验收标准：用户无需编辑配置即可一键发送正常、慢请求、错误、混合流量。

#### Task 2.3: 实现并发请求执行器和结果面板
- 文件：`examples/gateway-traffic-lab/frontend/src/App.jsx:1`
- 实现内容：实现浏览器端并发池、进度统计、成功/失败/平均耗时、最近响应列表。
- 验收标准：请求次数与网关监控请求量一致或接近，失败请求在错误 TopN 中可见。

### Sprint 3: 容器化和 Rainbond 部署

#### Task 3.1: 编写 Dockerfile
- 文件：`examples/gateway-traffic-lab/Dockerfile:1`
- 实现内容：前端构建后复制到 Go 后端静态目录，最终镜像只运行 Go 服务。
- 验收标准：`docker build` 成功，容器启动后访问 `/` 返回前端页面。

#### Task 3.2: 编写 Rainbond 部署说明
- 文件：`examples/gateway-traffic-lab/README.md:1`
- 实现内容：说明 Rainbond 部署、网关访问、监控插件同步、验证步骤。
- 验收标准：按文档部署后，点击页面按钮能在监控插件中看到请求量、错误、延迟和内部路由数据。

#### Task 3.3: 提供可选 Rainbond 应用描述
- 文件：`examples/gateway-traffic-lab/rainbond.yaml:1`
- 实现内容：提供组件端口、健康检查和环境变量示例。
- 验收标准：可作为 Rainbond 部署参数参考。

## 七、关键参考代码

| 功能 | 文件 | 说明 |
|------|------|------|
| 插件 HTTP server | `pkg/server/server.go` | 当前插件 API 和 collector 路由注册方式 |
| APISIX log model | `pkg/model/collector.go` | 监控采集依赖的状态码、延迟、URI 字段 |
| Collector 聚合逻辑 | `pkg/service/collector.go` | URI 分组、错误计数、延迟聚合规则 |
| 默认路由分组 | `cmd/plugin/main.go` | `/api/user/setting/*`、`/api/order/detail/*` 示例 |
| 插件代理和同步说明 | `README.md` | Route 级 `http-logger` 同步和访问链路 |

