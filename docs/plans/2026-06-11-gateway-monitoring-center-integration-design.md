# 网关监测融合监控中心设计文档

## 一、项目背景

### 1.1 项目架构

当前网关监测插件由两部分组成：

- `rainbond-plugin-template`：Go 后端，提供插件静态资源、网关访问日志采集、平台/应用/组件网络监控 API。
- `rainbon-Gateway-monitoring-ui`：插件前端，提供平台级、应用级、组件级网关监测页面。

监控中心当前前端位于 `rainbond-enterprise-base`，主要能力包括：

- 企业/集群概览卡片。
- Grafana 监控面板 iframe。
- 多集群 tabs。
- 资源分析 Sankey。

本次迁移目标是将监控中心的 Grafana 监控面板能力融合进网关监测平台级页面，并使用网关监测插件自身作为 Grafana 代理入口。

### 1.2 现有基础

`rainbond-ui` 已支持平台级插件多集群选择：

- 入口 URL 带 `showSelect=true` 时，`src/pages/RbdPlugins/index.js` 会显示集群选择框。
- 切换集群时，宿主会将 `regionName` 写入 URL。
- 插件组件会以 `key={regionName}` 重新挂载。

因此网关监测插件内部不需要再维护监控中心原有的集群 tabs，只需要读取当前 `regionName`。

### 1.3 核心需求

1. 将监控中心页面以 Tabs 形式融合进网关监测平台级页面。
2. 平台页保留现有网关流量监控能力。
3. 监控中心不再使用 `/proxy/plugins/rainbond-observability/...`。
4. Grafana 组件迁移到网关监测应用下，并由 `rainbond-gateway-monitoring` 代理。
5. 多集群切换由 `rainbond-ui` 的 `showSelect=true` 机制提供。
6. 资源分析 Sankey 不迁移。

## 二、整体架构设计

### 2.1 系统架构图

```text
rainbond-ui
  /enterprise/{eid}/plugins/rainbond-gateway-monitoring?regionName={region}&showSelect=true
      |
      v
rainbond-gateway-monitoring 前端
  Platform Tabs
    - 网关流量
    - 监控中心
      |
      v
  iframe:
  /console/regions/{region}/proxy/plugins/rainbond-gateway-monitoring/grafana/d/...
      |
      v
rainbond-console RainbondPluginFullProxyView
      |
      v
rainbond region PluginBackendProxy
      |
      v
rainbond-gateway-monitoring Go 后端
  /grafana/*
      |
      v
Grafana 组件
  gateway-monitoring-grafana
```

### 2.2 核心流程

1. 用户从企业菜单进入网关监测平台页。
2. `rainbond-ui` 在 URL 中追加 `showSelect=true`，显示集群选择器。
3. 用户切换集群后，宿主更新 `regionName` 并重新挂载插件。
4. 网关监测平台页读取当前 `regionName`。
5. `网关流量` tab 继续调用现有 `/backend/plugins/rainbond-gateway-monitoring/api/v1/...`。
6. `监控中心` tab iframe 指向 `/proxy/plugins/rainbond-gateway-monitoring/grafana/...`。
7. 网关监测 Go 后端将 `/grafana/*` 反向代理到同应用下的 Grafana 组件。

## 三、数据模型设计

### 3.1 新增数据库表

本次不新增数据库表。

### 3.2 数据关系

本次主要新增运行时关系：

| 对象 | 说明 |
|------|------|
| `regionName` | 由 `rainbond-ui` URL 参数提供，决定当前集群 |
| `rainbond-gateway-monitoring` Go 后端 | 网关监测 API 与 Grafana Web 代理入口 |
| `gateway-monitoring-grafana` 组件 | Grafana 服务，作为网关监测应用下的组件存在 |
| `NM_GRAFANA_BASE_URL` | Go 后端访问 Grafana 组件的内部地址 |

建议 Grafana 作为网关监测应用内的独立组件部署，而不是内嵌到 Go 二进制中。

## 四、API设计

### 4.1 接口列表

保留现有网关监测 API：

```text
GET /api/v1/platform/overview
GET /api/v1/platform/overview/trend
GET /api/v1/platform/apps/top-errors
GET /api/v1/platform/apps/top-latency
GET /api/v1/platform/apps/top-throughput
GET /api/v1/platform/nodes/summary
```

新增 Grafana 代理入口：

```text
GET    /grafana/*
POST   /grafana/*
PUT    /grafana/*
PATCH  /grafana/*
DELETE /grafana/*
```

前端访问路径：

```text
/console/regions/{region}/proxy/plugins/rainbond-gateway-monitoring/grafana/{grafana_path}
```

插件实际收到：

```text
/grafana/{grafana_path}
```

Go 后端代理到：

```text
{NM_GRAFANA_BASE_URL}/{grafana_path}
```

### 4.2 请求/响应结构

Grafana 代理不定义 JSON 响应结构，必须完整转发 Grafana 的：

- HTML
- CSS
- JavaScript
- 图片/字体等静态资源
- Grafana API 请求
- 响应状态码
- 必要响应头

需要重点处理：

- `Location` 重定向头。
- `Set-Cookie` 的 `Path`。
- Grafana 子路径访问。
- iframe 中静态资源相对路径。

## 五、核心实现设计

### 5.1 关键逻辑

#### 5.1.1 rainbond-ui 菜单入口

在企业菜单中识别 `rainbond-gateway-monitoring` 平台插件，入口地址追加 `showSelect=true`：

```text
/enterprise/{eid}/plugins/rainbond-gateway-monitoring?regionName={regionName}&showSelect=true
```

这样多集群切换完全由宿主负责，网关监测前端只响应当前集群。

#### 5.1.2 网关监测平台页 Tabs

平台页从单页结构调整为 Tabs：

- `网关流量`：现有平台级网关监测页面。
- `监控中心`：Grafana dashboard iframe 页面。

监控中心迁移内容：

- 集群概览
- 团队监控
- 节点监控
- 节点性能
- Pod 监控
- Pod 性能
- 守护进程监控
- 工作负载监控
- 无状态应用监控
- 有状态应用监控

不迁移：

- 资源分析 Sankey
- 监控中心内部多集群 tabs
- 对 `rainbond-observability` 插件列表的查询逻辑

#### 5.1.3 Grafana 代理

Go 后端新增 `/grafana/*` 代理处理。

建议环境变量：

```text
NM_GRAFANA_BASE_URL=http://gateway-monitoring-grafana.rbd-system.svc:3000
```

后端启动时读取并校验该地址。如果未配置：

- `/grafana/*` 返回 503。
- 前端监控中心 tab 展示 Grafana 服务未配置提示。

Grafana 推荐配置：

```text
GF_SERVER_ROOT_URL=%(protocol)s://%(domain)s/console/regions/{region}/proxy/plugins/rainbond-gateway-monitoring/grafana/
GF_SERVER_SERVE_FROM_SUB_PATH=true
```

如果 Grafana 无法使用动态 `{region}` root url，则 Go 后端需要对 `Location` 和部分资源路径做重写。

### 5.2 复用现有代码

| 来源 | 复用内容 |
|------|----------|
| `rainbond-ui/src/pages/RbdPlugins/index.js` | `showSelect=true` 集群选择逻辑 |
| `rainbond-enterprise-base/src/pages/content/index.js` | Grafana dashboard tabs 配置 |
| `rainbon-Gateway-monitoring-ui/src/pages/content/index.js` | 现有平台级网关流量页面 |
| `rainbond-plugin-template/pkg/server/server.go` | 插件后端路由、授权、HTTP server 结构 |
| `rainbond-console/console/views/rbd_plugin.py` | 现有完整 Web 应用代理链路 |

## 六、实施计划

### Sprint 1: Grafana 代理能力

#### Task 1.1: 增加 Grafana 代理配置

- 文件：`cmd/plugin/config.go`
- 文件：`cmd/plugin/main.go`
- 实现内容：
  - 新增 `NM_GRAFANA_BASE_URL` 配置读取。
  - 将 Grafana 地址传入 `server.Config`。
- 验收标准：
  - 未配置时服务可启动。
  - 配置非法 URL 时日志给出明确提示。

#### Task 1.2: 新增后端 Grafana 代理路由

- 文件：`pkg/server/server.go`
- 实现内容：
  - 新增 `/grafana/` 路由。
  - 使用 `httputil.NewSingleHostReverseProxy` 代理 Grafana。
  - 支持常见 HTTP 方法。
  - 保留 Grafana 响应头与状态码。
  - 重写 `Location`、`Set-Cookie Path`。
- 验收标准：
  - `/grafana/d/...` 可打开 Grafana dashboard。
  - 静态资源能正常加载。
  - iframe 内切换 dashboard 不脱离插件代理路径。

#### Task 1.3: 后端测试

- 文件：`pkg/server/grafana_proxy_test.go`
- 实现内容：
  - 测试路径裁剪。
  - 测试未配置 Grafana 返回 503。
  - 测试代理请求方法和查询参数透传。
  - 测试 `Location` 重写。
- 验收标准：
  - `go test ./pkg/server/...` 通过。

### Sprint 2: 平台前端融合

#### Task 2.1: 平台页拆分 Tabs

- 仓库：`rainbon-Gateway-monitoring-ui`
- 文件：`src/pages/content/index.js`
- 实现内容：
  - 将现有平台页内容放入 `网关流量` tab。
  - 新增 `监控中心` tab。
- 验收标准：
  - 默认仍展示网关流量。
  - 切换 tab 不影响现有网关监测刷新逻辑。

#### Task 2.2: 迁移 Grafana dashboard tabs

- 仓库：`rainbon-Gateway-monitoring-ui`
- 文件：`src/pages/content/index.js`
- 文件：`src/pages/content/index.less`
- 实现内容：
  - 迁移监控中心 dashboard 配置。
  - iframe src 改为 `/proxy/plugins/rainbond-gateway-monitoring/grafana/...`。
  - 移除 `rainbond-observability` 插件查询。
  - 移除 Sankey 相关 UI 和状态。
- 验收标准：
  - 监控中心 tab 可以加载 Grafana 页面。
  - 不再出现资源分析 tab。
  - URL 中切换 `regionName` 后 iframe 指向新集群路径。

#### Task 2.3: 前端构建与静态资源同步

- 仓库：`rainbon-Gateway-monitoring-ui`
- 仓库：`rainbond-plugin-template`
- 实现内容：
  - 执行 `npm run build`。
  - 将 `dist/main.js` 同步到 `rainbond-plugin-template/cmd/plugin/static/main.js`。
- 验收标准：
  - 插件通过 SystemJS 正常加载。
  - 平台、应用、组件三个视图不回归。

### Sprint 3: 宿主入口调整

#### Task 3.1: 企业菜单追加 showSelect

- 仓库：`rainbond-ui`
- 文件：`src/common/enterpriseMenu.js`
- 实现内容：
  - 识别 `rainbond-gateway-monitoring` 平台插件。
  - 入口路径追加 `showSelect=true`。
- 验收标准：
  - 企业菜单进入网关监测时显示集群选择器。
  - 切换集群后插件重新加载并使用新 `regionName`。

#### Task 3.2: 宿主构建验证

- 仓库：`rainbond-ui`
- 实现内容：
  - 执行前端构建。
- 验收标准：
  - `rainbond-ui` 构建通过。
  - 现有告警、日志等多集群插件入口不受影响。

### Sprint 4: 联调验证

#### Task 4.1: 单集群验证

- 验收标准：
  - 网关流量 tab 数据正常。
  - 监控中心 tab Grafana 正常加载。
  - Grafana 静态资源、API 请求无 404。

#### Task 4.2: 多集群验证

- 验收标准：
  - `showSelect=true` 显示集群选择器。
  - 切换集群后网关流量 API 使用新 region。
  - 监控中心 iframe 使用新 region 的 gateway monitoring 代理路径。

#### Task 4.3: 异常场景验证

- 验收标准：
  - Grafana 组件未配置时，页面展示可理解的错误提示。
  - Grafana 不可达时，不影响网关流量 tab。
  - 授权失败时仍按现有插件授权逻辑返回 403。

## 七、关键参考代码

| 功能 | 文件 | 说明 |
|------|------|------|
| 多集群插件选择器 | `rainbond-ui/src/pages/RbdPlugins/index.js` | `showSelect=true` 展示集群选择框，切换后更新 `regionName` |
| 企业插件菜单 | `rainbond-ui/src/common/enterpriseMenu.js` | 告警/日志插件已有追加 `showSelect=true` 的模式 |
| 插件完整 Web 代理 | `rainbond-console/console/views/rbd_plugin.py` | `RainbondPluginFullProxyView` 用于代理 Grafana 类 Web 应用 |
| Region 插件代理 | `rainbond/api/api_routers/version2/v2Routers.go` | `PluginBackendProxy` 裁剪 `/backend/plugins/{plugin}` 前缀并转发到插件后端 |
| 网关监测后端路由 | `rainbond-plugin-template/pkg/server/server.go` | 新增 `/grafana/*` 代理入口的位置 |
| 网关监测插件配置 | `rainbond-plugin-template/cmd/plugin/config.go` | 新增 Grafana 代理地址配置 |
| 网关监测平台页 | `rainbon-Gateway-monitoring-ui/src/pages/content/index.js` | 平台页 Tabs 融合入口 |
| 监控中心旧实现 | `rainbond-enterprise-base/src/pages/content/index.js` | dashboard tabs 与旧 iframe 拼接逻辑 |

## 风险与约束

1. Grafana 代理对路径和响应头敏感，必须联调静态资源、API、重定向、Cookie。
2. Grafana 建议作为网关监测应用下的独立组件，不建议内嵌到 Go 二进制。
3. 如果 Grafana 不能配置 `serve_from_sub_path`，后端需要更多路径重写，风险增加。
4. 监控中心原有 overview token 接口和 Sankey 不纳入本次迁移范围。
5. 本次迁移后，监控中心入口依赖 `rainbond-gateway-monitoring` 插件安装状态，而不再依赖 `rainbond-observability` 前端插件入口。
