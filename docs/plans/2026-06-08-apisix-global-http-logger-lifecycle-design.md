# APISIX GlobalRule 网关日志采集生命周期设计文档

## 一、项目背景

### 1.1 项目架构

当前 `rainbond-plugin-template` 中的网关监控插件由三段链路组成：

```text
APISIX access log
  -> http-logger
     -> rainbond-gateway-monitoring /api/v1/collector/apisix/logs
        -> Redis 5 秒 bucket 和窗口快照
           -> 插件前端平台、应用、组件视图
```

插件后端同时负责 APISIX Route 映射扫描，把 APISIX `route_id` 转换为 Rainbond 的团队、应用、组件和路由维度。前端通过 Rainbond 插件代理访问 `/api/v1/*` 接口，不直接访问插件 Service。

现有实现默认在应用页面触发 Route 级 `http-logger` 同步：后端扫描团队 namespace 下的 `ApisixRoute`，匹配 Rainbond 管理的路由，然后把 `http-logger` 追加到对应 `ApisixRoute.spec.http[*].plugins`。

### 1.2 现有基础

当前代码已经具备以下基础能力：

- `pkg/server`：提供 collector、监控查询和应用级 `http-logger` 同步入口。
- `pkg/gateway`：扫描 `ApisixRoute`、匹配 Rainbond app/component、保存 route 映射。
- `pkg/service` 和 `pkg/repository`：将 APISIX 日志聚合成平台、团队、应用、组件、内部路由指标。
- `deploy/rbdplugin.yaml`：提供 RBDPlugin、ServiceAccount 和 APISIX 相关 RBAC 示例。
- Rainbond 当前前端没有平台插件启停入口，因此不能把 `plugin.rainbond.io/enable` 标签作为采集开关来源。

这些能力可以继续复用，但采集开启方式需要从“修改业务路由”调整为“插件独立资源控制”。

### 1.3 核心需求

- 安装插件后即可开启网关访问日志采集，不依赖用户进入某个应用页面。
- 插件停止或卸载时能够尽量关闭采集，避免 APISIX 继续向不可用的插件推送日志。
- 默认不修改用户已有 `ApisixRoute`，避免覆盖用户自定义插件配置。
- 支持跨团队、跨应用的数据采集与排行。
- 环境变量应尽量自动发现，手动配置只作为覆盖项。
- 保留 Route 级挂载作为显式兼容模式，便于旧集群或不支持 GlobalRule 的环境降级。

## 二、整体架构设计

### 2.1 系统架构图

```text
Rainbond platform plugin install
  -> create plugin app
  -> create RBDPlugin rainbond-gateway-monitoring

rainbond-gateway-monitoring backend
  -> check local runtime and collector readiness
  -> scan Rainbond-managed ApisixRoute
  -> discover namespace + ingressClassName
  -> reconcile ApisixGlobalRule http-logger
  -> read-only route mapping scanner

APISIX
  -> GlobalRule http-logger
     -> collector URI
        -> Redis hot window aggregation
           -> platform/app/component monitoring UI
```

### 2.2 核心流程

运行流程：

1. 平台安装插件，Rainbond 创建插件应用和 `RBDPlugin`。
2. 插件后端启动后完成授权、Redis、Collector 路由注册和基础健康检查。
3. 如果插件进程运行且 Collector ready，后端扫描 `ApisixRoute`，找出 Rainbond 管理的 route 所在 namespace 和 `ingressClassName`。
4. 后端按 namespace 和 ingress class 创建或更新插件专属 `ApisixGlobalRule`。
5. APISIX 通过 GlobalRule 上的 `http-logger` 把访问日志发送到 collector。
6. 后端只读扫描 route 映射，保存 `route_id -> team/app/component/route` 的关系。
7. 前端平台、应用、组件视图读取 Redis 聚合数据。

停止流程：

1. 插件进程收到 SIGTERM 或 preStop 触发优雅退出。
2. 后端停止创建新的 GlobalRule，并删除由本插件管理的 `ApisixGlobalRule`。
3. 后端等待 APISIX GlobalRule 删除完成或直到优雅退出超时。
4. APISIX 停止向 collector 推送全局访问日志。
5. 已有 Redis 窗口数据自然过期。

异常崩溃场景下，插件进程无法主动删除 Kubernetes 资源。该场景需要依赖下次启动时清理残留 GlobalRule，或后续在 Rainbond 平台侧补充卸载钩子 / 外部清理控制器。

降级流程：

1. 如果集群不存在 `ApisixGlobalRule` CRD，或 RBAC 不允许管理 GlobalRule，插件记录明确 warning。
2. 默认不自动回退到 Route 级写入，避免用户路由被意外修改。
3. 只有显式配置 `NM_HTTP_LOGGER_MODE=route` 时才启用旧的 Route 级挂载逻辑。

## 三、数据模型设计

### 3.1 新增数据库表

无需新增数据库表。

采集方式变更不改变现有 Redis 数据模型：

- 5 秒 bucket 仍作为 collector 写入的最小时间分片。
- 5m / 10m / 30m 窗口快照仍由后台聚合任务生成。
- route 映射仍保存 APISIX route 与 Rainbond team/app/component 的关系。

新增 Kubernetes 资源由 APISIX CRD 承载：

```yaml
apiVersion: apisix.apache.org/v2
kind: ApisixGlobalRule
metadata:
  name: rainbond-gateway-monitoring-http-logger
  namespace: <discovered-namespace>
  labels:
    app.kubernetes.io/managed-by: rainbond-gateway-monitoring
    network-monitor.rainbond.io/global-rule: "true"
spec:
  plugins:
  - name: http-logger
    enable: true
    config:
      uri: <collector-uri>
      timeout: 3
      ssl_verify: false
```

实际 `apiVersion` 需要以后端动态资源发现结果为准；实现时应优先复用集群内已有 APISIX CRD 版本。

### 3.2 数据关系

GlobalRule 负责采集所有匹配 APISIX ingress class 的访问日志，但日志本身仍依赖 `route_id` 做 Rainbond 维度归属：

```text
ApisixGlobalRule
  -> APISIX access log
     -> route_id
        -> route mapping cache
           -> team_id/team_name
           -> app_id/app_name/region_app_id
           -> component_id/component_name/service_alias
           -> prometheus_route/internal_route
```

GlobalRule 不保存业务维度；业务维度仍由只读 route mapping scanner 从 `ApisixRoute` 标签、名称和 Rainbond 上下文中解析。

## 四、API设计

### 4.1 接口列表

本设计不要求新增对外业务 API。现有接口继续保留：

| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/api/v1/collector/apisix/logs` | APISIX `http-logger` Collector |
| GET | `/api/v1/platform/overview` | 平台总览 |
| GET | `/api/v1/platform/overview/trend` | 平台趋势 |
| GET | `/api/v1/platform/apps/top-errors` | 应用错误排行 |
| GET | `/api/v1/platform/apps/top-latency` | 应用延迟排行 |
| GET | `/api/v1/platform/teams/top-throughput` | 团队吞吐率排行 |
| GET | `/api/v1/apps/{app_id}/overview` | 应用总览 |
| GET | `/api/v1/apps/{app_id}/overview/trend` | 应用趋势 |
| GET | `/api/v1/components/{component_id}/overview` | 组件总览 |
| GET | `/api/v1/components/{component_id}/overview/trend` | 组件趋势 |
| POST | `/api/v1/apps/{app_id}/gateway/http-logger/sync` | 兼容接口，仅在 route 模式下执行 Route 级同步 |

### 4.2 请求/响应结构

本插件不依赖 Rainbond 插件状态接口作为采集开关。采集生命周期由插件后端自身运行状态和 Collector readiness 控制。

`/api/v1/apps/{app_id}/gateway/http-logger/sync` 在 GlobalRule 默认模式下应返回兼容结果，说明当前采集由 GlobalRule 管理：

```json
{
  "mode": "global",
  "synced": false,
  "message": "http-logger is managed by ApisixGlobalRule"
}
```

当显式启用 route 模式时，该接口沿用现有请求结构：

```json
{
  "namespace": "xuzl",
  "region_app_id": "0d6ac4cabd714b308baaa033b84b387f"
}
```

## 五、核心实现设计

### 5.1 关键逻辑

#### 5.1.1 采集模式

新增采集模式配置：

| 模式 | 含义 |
|------|------|
| `global` | 默认模式，使用 `ApisixGlobalRule` 管理 `http-logger` |
| `route` | 兼容模式，沿用 Route 级挂载 |
| `off` | 不自动管理 APISIX `http-logger`，只保留 collector 和查询接口 |

默认值为 `global`。

#### 5.1.2 自动发现

插件运行后自动发现以下信息：

- namespace：扫描 Rainbond 管理的 `ApisixRoute` 所在 namespace。
- ingress class：优先读取 `ApisixRoute.spec.ingressClassName`，为空时使用集群默认 APISIX ingress class，最后兜底为 `apisix`。
- collector URI：不暴露手动配置项；插件启动时读取自身容器 `eth0` IPv4，并生成 `http://{podIP}:8080/api/v1/collector/apisix/logs`，无法获取时回退到第一个非 loopback IPv4，再失败才使用默认 Service URI。

环境变量只作为覆盖项：

- `NM_APISIX_GLOBAL_RULE_NAMESPACE`：固定 GlobalRule namespace。
- `NM_APISIX_INGRESS_CLASS`：固定 ingress class。
- `NM_HTTP_LOGGER_GLOBAL_RULE_NAME`：固定 GlobalRule 名称。

#### 5.1.3 GlobalRule reconcile

后端周期性执行 reconcile：

1. 检查插件进程运行状态、授权状态和 Collector readiness。
2. 扫描 `ApisixRoute`，过滤 Rainbond 管理的 route。
3. 按 `namespace + ingressClassName` 分组。
4. 插件可接收日志时，为每组创建或更新 `ApisixGlobalRule`。
5. 插件不可接收日志、`NM_HTTP_LOGGER_MODE=off` 或进程优雅停止时，删除带有插件管理 label 的 `ApisixGlobalRule`。
6. 若 APISIX CRD 不支持 GlobalRule，记录 warning，并保持查询接口可用。

GlobalRule 必须带有稳定 label 和 annotation，保证只删除自己管理的资源，不影响用户创建的 GlobalRule。

#### 5.1.4 Route 映射扫描

Route 映射扫描从“写入路由插件 + 保存映射”拆分为“只读扫描 + 保存映射”：

- 读取 `ApisixRoute`。
- 识别 Rainbond 管理标签。
- 解析 app、team、component、service alias、prometheus route。
- 保存 route mapping 和 app route index。
- 不修改 `ApisixRoute.spec`。

#### 5.1.5 重复日志处理

如果用户已经在某些 Route 上手动配置 `http-logger`，GlobalRule 可能让同一次请求产生重复日志。实现时按优先级处理：

1. 如果日志中存在 request id，collector 在短 TTL 内做 request id 去重。
2. 如果没有 request id，仅记录 warning，并在 README 中说明不要同时配置 route-level 和 global-level `http-logger`。

#### 5.1.6 与插件运行状态结合

当前 Rainbond 前端没有平台插件启停入口，因此不能依赖 `plugin.rainbond.io/enable` 判断是否采集。插件后端应以自身运行状态作为 GlobalRule 生命周期来源：

- running 且 collector ready：确保 GlobalRule 存在。
- `NM_HTTP_LOGGER_MODE=off`：删除已管理的 GlobalRule，不再自动创建。
- collector 不可用：删除已管理的 GlobalRule 或跳过创建，避免 APISIX 持续发送到不可用地址。
- SIGTERM / preStop 优雅退出：删除已管理的 GlobalRule 后退出。

该方案不需要修改 rainbond 或 rainbond-console，也不需要前端提供插件启停入口。限制是：如果插件进程异常崩溃或节点直接失联，进程没有机会删除 GlobalRule。必须通过下次启动清理残留资源，或在后续版本中引入 Rainbond 平台卸载钩子 / 外部清理控制器解决强一致清理问题。

### 5.2 复用现有代码

建议复用和调整以下代码：

- `cmd/plugin/main.go`：启动采集模式选择、GlobalRule reconcile job、兼容 route job。
- `pkg/gateway/job.go`：保留 route 模式逻辑，抽出只读 scanner。
- `pkg/gateway/http_logger.go`：复用 http-logger config 构造逻辑。
- `pkg/repository/route_mapping.go`：继续保存 route mapping 和 app route index。
- `pkg/server/server.go`：兼容同步接口在 global 模式下返回模式说明。
- `deploy/rbdplugin.yaml`：RBAC 从默认更新 `apisixroutes` 调整为管理 `apisixglobalrules`，只读 `apisixroutes`。
- `README.md`：更新默认策略、环境变量、故障排查和兼容模式说明。

## 六、实施计划

### Sprint 1: 文档和部署模型收敛

#### Task 1.1: 新增 GlobalRule 生命周期设计文档
- 文件：`docs/plans/2026-06-08-apisix-global-http-logger-lifecycle-design.md:1`
- 实现内容：记录默认 GlobalRule 方案、插件运行状态驱动流程、自动发现、RBAC 和实施计划。
- 验收标准：文档覆盖插件运行/停止、APISIX GlobalRule、Route 级兼容模式。

#### Task 1.2: 更新 README 采集策略说明
- 文件：`README.md:1`
- 实现内容：将默认策略从 Route 级挂载改为 GlobalRule，补充环境变量和兼容模式。
- 验收标准：README 不再声明默认修改 `ApisixRoute`，并说明不会覆盖用户路由插件配置。

#### Task 1.3: 更新部署 RBAC 示例
- 文件：`deploy/rbdplugin.yaml:23`
- 实现内容：增加 `apisixglobalrules` 管理权限，将 `apisixroutes` 改为只读。
- 验收标准：默认 RBAC 支持 GlobalRule reconcile，不要求默认更新业务 `ApisixRoute`。

### Sprint 2: 后端 GlobalRule reconcile

#### Task 2.1: 增加采集模式配置
- 文件：`cmd/plugin/main.go:1`
- 实现内容：读取 `NM_HTTP_LOGGER_MODE`，支持 `global`、`route`、`off`。
- 验收标准：默认值为 `global`，非法值启动时报 warning 并回落到 `global`。

#### Task 2.2: 实现 APISIX GlobalRule 客户端
- 文件：`pkg/gateway/global_rule.go:1`
- 实现内容：用 dynamic client 创建、更新、删除 `ApisixGlobalRule`，支持 CRD 版本发现。
- 验收标准：单元测试覆盖 resource manifest、managed label、删除保护。

#### Task 2.3: 实现 GlobalRule reconcile job
- 文件：`pkg/gateway/global_rule_job.go:1`
- 实现内容：检查 collector readiness，扫描 route 分组，运行时 upsert，off 模式或优雅停止时 delete。
- 验收标准：单元测试覆盖运行、off 模式、collector 不可用、无 CRD、RBAC 失败、namespace override。

#### Task 2.4: 拆分只读 route mapping scanner
- 文件：`pkg/gateway/job.go:1`
- 实现内容：把 route mapping 生成与 route-level plugin 写入解耦。
- 验收标准：GlobalRule 模式下不调用 `Update` 修改 `ApisixRoute`。

### Sprint 3: 兼容模式和采集可靠性

#### Task 3.1: 调整应用级同步接口
- 文件：`pkg/server/server.go:428`
- 实现内容：GlobalRule 模式下同步接口返回兼容信息，route 模式下执行旧逻辑。
- 验收标准：前端旧调用不会报错，也不会在默认模式修改路由。

#### Task 3.2: 增加 collector 去重能力
- 文件：`pkg/service/collector.go:1`
- 实现内容：如果日志中存在 request id，短 TTL 去重，降低 GlobalRule 与手动 route logger 并存时重复计数风险。
- 验收标准：单元测试覆盖重复 request id 只计一次。

#### Task 3.3: 增加运行日志和排障信息
- 文件：`cmd/plugin/main.go:1`
- 实现内容：启动时输出采集模式、collector URI、GlobalRule name、发现的 namespace 和 ingress class。
- 验收标准：用户能通过插件日志判断当前采集发往哪里、是否启用 GlobalRule。

## 七、关键参考代码

| 功能 | 文件 | 说明 |
|------|------|------|
| 插件入口和后台任务 | `cmd/plugin/main.go` | 当前启动 route-level attach job 和 collector 聚合任务 |
| Route 级同步 job | `pkg/gateway/job.go` | 当前扫描 APISIXRoute、挂载 http-logger、保存映射 |
| http-logger 配置构造 | `pkg/gateway/http_logger.go` | 可复用插件 config 构造和 managed annotation 常量 |
| 应用同步 API | `pkg/server/server.go` | 当前 `/gateway/http-logger/sync` 处理入口 |
| Redis route mapping | `pkg/repository/route_mapping.go` | 保存 route_id 到 Rainbond 维度映射 |
| RBDPlugin 代理配置 | `/Users/guox/Desktop/Project/rainbond/pkg/apis/rainbond/v1alpha1/rbdplugin_types.go` | RBDPlugin spec 中的 frontend_service / backend_service 仍用于插件代理 |
| 部署 RBAC | `deploy/rbdplugin.yaml` | 需要从 route update 权限转为 GlobalRule 管理权限 |
