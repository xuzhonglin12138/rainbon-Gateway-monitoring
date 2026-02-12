# Rainbond Plugin Template

Rainbond 平台插件开发骨架模板。基于此模板可快速开发 Rainbond 平台插件，只需替换 `main.js` 即可。

## 功能

- `go:embed` 将前端 JS 打包进 Go 二进制
- 启动时读取 ConfigMap 校验 RSA 授权签名
- 定时重验证授权状态（默认每 60 分钟）
- 未授权时 `/static/main.js` 返回 403，`/healthz` 返回 503

## 项目结构

```
├── cmd/plugin/
│   ├── main.go          # 入口
│   ├── config.go        # 插件配置（plugin_id、端口等）
│   └── static/
│       └── main.js      # 替换为你的前端构建产物
├── pkg/
│   ├── license/         # 授权验证（LicenseToken + RSA 验签 + 定时重验）
│   └── server/          # HTTP 服务（/static/main.js + /healthz）
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

### 5. 部署

```bash
# 构建镜像
make docker-build

# 部署 RBDPlugin CRD 和 RBAC
kubectl apply -f deploy/rbdplugin.yaml
```

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

## 与 rbd-api 的集成

rbd-api 已有 `platformPluginsRouter` 实现，通过 RBDPlugin CRD 的 `fronted_path` 字段代理请求：

```
浏览器 → Console → rbd-api (PluginStaticProxy)
                       → 读 RBDPlugin CRD 获取 fronted_path
                       → HTTP GET → 本插件服务 /static/main.js
```
