# Rainbond Plugin Template

Rainbond 平台插件开发骨架模板。基于此模板可快速开发 Rainbond 平台插件，只需替换 `main.js` 即可。

## 功能

- `go:embed` 将前端 JS 打包进 Go 二进制
- 启动时读取 ConfigMap 校验 RSA 授权签名
- 定时重验证授权状态（默认每 60 分钟）
- 未授权时 `/static/main.js` 返回 403，`/healthz` 返回 503
- 网络监控 Collector：接收 APISIX `http-logger` 日志并写入 Redis 热窗口聚合
- Route 级挂载 APISIX `http-logger`：只处理 Rainbond 管理的 `ApisixRoute`
- Redis-only 存储：5 秒 bucket、5m / 10m / 30m 窗口、route_id 映射缓存

## 项目结构

```
├── cmd/plugin/
│   ├── main.go          # 入口
│   ├── config.go        # 插件配置（plugin_id、端口等）
│   └── static/
│       └── main.js      # 替换为你的前端构建产物
├── pkg/
│   ├── license/         # 授权验证（LicenseToken + RSA 验签 + 定时重验）
│   ├── server/          # HTTP 服务（/static/main.js + /healthz + /api/v1/*）
│   ├── model/           # 网络监控数据模型、窗口约束、Collector 日志结构
│   ├── service/         # Collector 聚合、route_group 归类
│   ├── repository/      # Redis-only 热窗口存储
│   └── gateway/         # Route 级 http-logger 挂载与 route_id 映射
├── deploy/
│   └── rbdplugin.yaml   # RBDPlugin CRD + RBAC 示例
├── Dockerfile
└── Makefile
```

## 快速开始

### 1. 复制项目并修改配置

修改 `cmd/plugin/config.go`：

```go
const (
    PluginID = "your-plugin-id"   // 改为你的插件 ID
)
```

### 2. 替换前端文件

将你的前端构建产物放到 `cmd/plugin/static/main.js`。

### 3. 构建

```bash
# 开发构建（需要 -public-key 参数指定公钥文件）
make build

# 生产构建（公钥内置到二进制）
make build-with-key
```

### 4. 运行

```bash
# 本地开发（需要 kubeconfig 和公钥文件）
./bin/rainbond-plugin-template \
    -kubeconfig=$HOME/.kube/config \
    -public-key=keys/public.pem

# 集群内运行（使用 in-cluster config，公钥编译内置）
./bin/rainbond-plugin-template
```

### 网络监控环境变量

| 变量 | 默认值 | 说明 |
|------|--------|------|
| `NM_REDIS_ADDR` | `127.0.0.1:6379` | Redis 地址 |
| `NM_REDIS_PASSWORD` | 空 | Redis 密码 |
| `NM_REDIS_DB` | `0` | Redis DB |
| `NM_REDIS_TIMEOUT_SECONDS` | `3` | Redis 操作超时 |
| `NM_REDIS_TLS` | `false` | 是否使用 TLS 连接 Redis |
| `NM_PROMETHEUS_URL` | `http://127.0.0.1:9999` | Prometheus API 地址 |
| `NM_PROMETHEUS_TIMEOUT_SECONDS` | `3` | Prometheus 查询超时 |
| `NM_DEFAULT_SLA_TARGET` | `0.999` | 默认应用 SLA 目标值 |
| `NM_APISIX_NAMESPACES` | 空 | 可选预热扫描 namespace；常规场景由前端按当前团队 namespace 调用同步接口 |
| `NM_COLLECTOR_URI` | `http://rainbond-gateway-monitoring/api/v1/collector/apisix/logs` | 写入到 APISIX `http-logger` 的 Collector 地址；需确保 APISIX 可访问 |
| `NM_HTTP_LOGGER_TIMEOUT_SECONDS` | `3` | APISIX `http-logger` 超时 |
| `NM_HTTP_LOGGER_SSL_VERIFY` | `false` | APISIX `http-logger` SSL 校验 |
| `NM_HTTP_LOGGER_SYNC_INTERVAL_SECONDS` | `60` | Route 挂载同步周期 |
| `NM_SNAPSHOT_REFRESH_SECONDS` | `5` | Redis TopN / summary 快照刷新周期 |
| `NM_ROUTE_GROUP_LIMIT` | `100` | 每个应用 route_group 基数上限 |

### 5. 部署

```bash
# 构建镜像
make docker-build

# 部署 RBDPlugin CRD 和 RBAC
kubectl apply -f deploy/rbdplugin.yaml
```

插件建议使用统一名称 `rainbond-gateway-monitoring`。这个名称需要同时体现在：

- Kubernetes Service 名称，例如 `rainbond-gateway-monitoring.rbd-system.svc:8080`
- RBDPlugin `metadata.name`
- 前端代理路径中的 `{pluginName}`

`pluginName` 以 RBDPlugin CR 的 `metadata.name` 为准，不是任意 display name。

## 授权验证流程

```
启动 → 读 ConfigMap rbd-system/rbd-license-info
     → RSA 公钥验签
     → 检查 plugin_mapping 包含本插件
     → 检查有效期
     → 通过：正常提供 main.js
     → 失败：返回 403，等待下次重验
```

## API

| 路径 | 说明 |
|------|------|
| `GET /static/main.js` | 返回嵌入的前端 JS（需授权） |
| `GET /healthz` | 健康检查（授权有效返回 200，否则 503） |
| `POST /api/v1/collector/apisix/logs` | APISIX `http-logger` Collector，接受单条或数组日志 |
| `GET /api/v1/platform/internal-routes/top-errors?window=5m` | 平台内部路由错误 TopN |
| `GET /api/v1/platform/internal-routes/top-latency?window=5m` | 平台内部路由延迟 TopN |
| `GET /api/v1/teams/{team_id}/internal-routes/top-errors?window=5m` | 团队内部路由错误 TopN |
| `GET /api/v1/teams/{team_id}/internal-routes/top-latency?window=5m` | 团队内部路由延迟 TopN |
| `GET /api/v1/apps/{app_id}/internal-routes/summary?window=5m` | 应用内部路由汇总 TopN |
| `GET /api/v1/apps/{app_id}/internal-routes/top-errors?window=5m` | 应用内部路由错误 TopN |
| `GET /api/v1/apps/{app_id}/internal-routes/top-latency?window=5m` | 应用内部路由延迟 TopN |
| `GET /api/v1/apps/{app_id}/sla?window=5m` | 应用 SLA，基于 `apisix_http_status` 入口 5xx 成功率计算 |
| `POST /api/v1/apps/{app_id}/gateway/http-logger/sync` | 按当前应用和团队 namespace 同步 Route 级 `http-logger` |
| `GET /api/v1/components/{component_id}/internal-routes?window=5m` | 组件内部路由 TopN |

`window` 只允许 `5m`、`10m`、`30m`，默认 `5m`。Collector 不保存 raw log，只写入 Redis 5 秒 bucket，bucket TTL 为 35 分钟。后台任务默认每 5 秒将 bucket 聚合成 `error-top`、`latency-top`、`request-top` 和 `summary` 快照，快照 TTL 为 120 秒，页面 API 读取快照返回。

## APISIX http-logger 挂载策略

本模板采用 Route 级挂载 APISIX `http-logger`，不使用 Global Rules，也不访问 APISIX Admin API。

`deploy/rbdplugin.yaml` 中的 RBAC 只作为最小示例。应用页面打开后，前端应使用当前团队上下文调用同步接口，而不是依赖固定环境变量：

```http
POST /api/v1/apps/{console_group_id}/gateway/http-logger/sync
Content-Type: application/json

{
  "namespace": "当前团队 currentTeam.namespace",
  "region_app_id": "当前应用 groupDetail.region_app_id"
}
```

后端使用 `namespace` 读取该团队下的 `ApisixRoute`，并优先用 `region_app_id` 匹配 APISIX Route 的 `metadata.labels.app_id`。这是因为 Rainbond Console 的 `group_id` 与 region 侧 `Application.app_id` 不是同一个标识。

如果希望插件启动后后台预热，也可以设置 `NM_APISIX_NAMESPACES`。该变量只是可选兜底，不适合作为多团队应用的唯一 namespace 来源。无论采用前端触发还是后台预热，都需要为对应业务团队 namespace 绑定 `apisixroutes get/list/update` 权限，或按部署模型改成受控的 ClusterRole。

当 `NM_APISIX_NAMESPACES` 非空时，后台任务会定期扫描这些 namespace 下由 Rainbond 管理的 `ApisixRoute`，判断规则包括：

- `creator=Rainbond` / `creator=rainbond`
- 存在 `app_id` 或 `service_alias` 标签
- 存在值为 `service_alias` 的组件别名标签

任务只追加或修正 `http-logger` 的 `enable`、`uri`、`timeout`、`ssl_verify` 字段，不删除、不覆盖其他插件配置。

## 与 rbd-api 的集成

rbd-api 已有 `platformPluginsRouter` 实现，通过 RBDPlugin CRD 的 `frontend_service` 和 `backend_service` 字段代理请求。

### 静态资源代理

前端插件 JS 由宿主通过以下路径加载：

```text
/console/regions/{region}/static/plugins/rainbond-gateway-monitoring
```

代理链路：

```text
浏览器
  -> rainbond-console
     /console/regions/{region}/static/plugins/{pluginName}
  -> rainbond
     /v2/platform/static/plugins/{pluginName}
  -> RBDPlugin.spec.frontend_service
     /static/main.js
```

### 后端 API 代理

插件前端调用业务 API 时不要直连插件 Service，统一走 Rainbond 插件代理入口：

```text
/console/regions/{region}/backend/plugins/rainbond-gateway-monitoring/api/v1/...
```

代理链路：

```text
rainbond-ui
  -> rainbond-console
     /console/regions/{region}/backend/plugins/{pluginName}/api/v1/...
  -> rainbond
     /v2/platform/backend/plugins/{pluginName}/api/v1/...
  -> RBDPlugin.spec.backend_service
     /api/v1/...
```

Rainbond 的 `PluginBackendProxy` 会裁掉 `/backend/plugins/{pluginName}/` 前缀，只把后面的路径转发给插件后端。因此：

```text
前端请求:
/console/regions/rainbond/backend/plugins/rainbond-gateway-monitoring/api/v1/apps/12/sla

插件实际收到:
/api/v1/apps/12/sla
```

按应用触发 Route 级 `http-logger` 同步时，前端应调用：

```http
POST /console/regions/{region}/backend/plugins/rainbond-gateway-monitoring/api/v1/apps/{console_group_id}/gateway/http-logger/sync
Content-Type: application/json

{
  "namespace": "currentTeam.namespace",
  "region_app_id": "groupDetail.region_app_id"
}
```

`namespace` 来自当前团队，`region_app_id` 用来匹配 APISIX Route 的 `metadata.labels.app_id`。不要把所有团队 namespace 固化进 `NM_APISIX_NAMESPACES`；该变量只适合启动时可选预热。
