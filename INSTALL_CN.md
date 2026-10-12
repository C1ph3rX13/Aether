<h1 align="center">安装与部署</h1>

<p align="center">从源码构建 Aether，准备 Docker，并启动首个项目。</p>

<p align="center">
  <a href="INSTALL.md">English</a> ·
  <a href="INSTALL_CN.md">简体中文</a> ·
  <a href="README_CN.md">返回 README</a>
</p>

> [!TIP]
> **推荐使用 `aether run` 启动**。该命令会同时启动 Server 和 Dispatcher。本指南面向 Linux；两个角色需要不同主机或权限时，可参考[分离部署](#分离-server-与-dispatcher)。

## 目录

- [环境要求](#环境要求)
- [构建与安装](#构建与安装)
- [准备 Worker 镜像](#准备-worker-镜像)
- [首次启动](#首次启动)
- [配置首个 Worker](#配置首个-worker)
- [分离 Server 与 Dispatcher](#分离-server-与-dispatcher)
- [健康状态与数据持久化](#健康状态与数据持久化)
- [验证](#验证)
- [常见问题](#常见问题)

## 环境要求

| 依赖 | 使用阶段 | 要求 |
| --- | --- | --- |
| Go | 构建 | 1.26.4 或更新版本，与 `go.mod` 声明一致。 |
| Node.js 和 npm | 构建 | Node.js 20.9 或更新版本，已使用 Node.js 24 验证。 |
| Bash、Git 和 Unix 工具 | 安装 | 包括 `install` 和 `mktemp`。 |
| Docker daemon 和 Worker 镜像 | 执行任务 | Dispatcher 能够访问 daemon，且对应镜像可用。 |
| 模型提供方凭据 | 执行任务 | 通过各 Worker 分别配置。 |

构建时需要访问 Go 模块、npm 包和 Google Fonts，执行任务时需要访问配置的模型提供方。安装后的二进制已包含前端，应用主机在构建完成后不再需要 Node.js。

## 构建与安装

```bash
git clone https://github.com/C1ph3rX13/Aether.git
cd Aether
./install.sh
export PATH="$HOME/.local/bin:$PATH"
aether --version
```

`aether --version` 应显示 **0.1.0**。将 `PATH` 设置加入 shell 配置，可使其在后续会话中生效。默认安装到 `~/.local/bin/aether`，也可指定其他可写目录：

```bash
./install.sh /path/to/bin
```

脚本执行 `npm ci`，构建静态前端，将其嵌入 Go 二进制，生成 `dist/aether` 后再安装。构建失败时会保留已经安装的旧二进制。脚本不安装环境依赖、不配置 Docker、不创建部署配置，也不注册系统服务。

## 准备 Worker 镜像

在 Dispatcher 将要连接的 Docker daemon 所在主机执行：

```bash
docker info
docker pull ghcr.io/c1ph3rx13/aether-worker:latest
docker image inspect ghcr.io/c1ph3rx13/aether-worker:latest
```

执行项目前，该 daemon 上必须已有对应镜像；Aether 不会自动拉取镜像。推荐配置使用上述镜像和 `host` 网络模式。自定义镜像须支持所选的 Claude Code、Codex 或 Pi 运行环境。本仓库包含应用源码，不包含 Worker 镜像构建定义。

## 首次启动

新建部署时，在仓库根目录执行以下命令，创建私有数据目录和本地部署配置：

```bash
mkdir -p data
chmod 700 data
cat > configs/bootstrap-deployment.toml <<EOF
schema = "aether.bootstrap/v1"

[deployment]
id = "aether-local"

[operator]
name = "local-operator"

[server]
listen_host = "127.0.0.1"
listen_port = 8000
database_path = "$PWD/data/aether.db"

[dispatcher]
control_plane_url = "http://127.0.0.1:8000/api/v1"
docker_host = "unix:///var/run/docker.sock"
EOF
```

使用推荐命令启动 Aether：

```bash
aether run --deployment "$PWD/configs/bootstrap-deployment.toml"
```

打开 **[http://127.0.0.1:8000](http://127.0.0.1:8000)**。程序在前台运行，按 Ctrl+C 停止。

示例中的 `$PWD` 由 shell 在写入文件时展开。Aether 不会展开配置值中的环境变量或 `~`，`database_path` 必须是绝对路径。如果 Docker socket 不在示例位置，请在 `docker_host` 填写实际的绝对路径；程序不会回退读取 `DOCKER_HOST`。

同一数据库须保持部署 ID 稳定；共享 Docker daemon 的独立部署须使用不同 ID。将服务监听在可信接口上。应用 API 没有内置登录，远程访问应通过带身份验证的访问层。

`data/` 和 `configs/bootstrap-deployment.toml` 已被 Git 忽略。不要将 Worker 配置或 API Key 写进部署文件；它只支持[示例配置](configs/bootstrap-deployment.example.toml)中的字段。

## 配置首个 Worker

> [!IMPORTANT]
> 新数据库没有生效配置。此时可以访问界面，执行部分会等待配置完成。

1. 打开 **Workers**，选择 Claude Code、Codex 或 Pi，再点击 **New Worker**。
2. 填写唯一名称、正整数容量和任务类型。初次配置可在同一个 Worker 上勾选 **Bootstrap**、**Reason**、**Explore**。所有 Worker 合计必须覆盖这三种任务，才能创建项目。
3. 进入 **Provider**，选择提供方配置，并填写表单要求的端点、模型和凭据。
4. 检查后点击 **Save Worker**。保存会检查提供方；如有校验或连接错误，先按提示修正。
5. 检查 **Runtime**、**Tasks** 和 **Prompts**。在 **Runtime → Project** 中确认 Worker 镜像和网络模式符合 Docker 环境。设置页面提供推荐值。
6. 等待出现 **Project creation ready**，然后进入 **Projects → New Project**，填写标题、Origin 和 Goal。

**Origin** 填写已知的初始上下文，**Goal** 填写希望达成的结果。创建项目需要已保存的配置覆盖 Bootstrap、Reason 和 Explore。如果 Dispatcher 暂时不可用，仍可创建项目，执行会等待它恢复。

| 适配器 | 主要提供方字段 | 凭据 |
| --- | --- | --- |
| Claude Code | `ANTHROPIC_BASE_URL`、`ANTHROPIC_MODEL`、`IS_SANDBOX` | `ANTHROPIC_AUTH_TOKEN` |
| Codex | 支持 Responses API 的 `CODEX_BASE_URL`、`CODEX_MODEL` | `OPENAI_API_KEY` |
| Pi | `PI_BASE_URL`、`PI_MODEL`、`PI_PROVIDER_API` | `PI_API_KEY` |

Pi 支持 `anthropic-messages`、`openai-responses`、`openai-completions` 三种提供方 API 模式。这些字段通过 Worker 编辑器设置。凭据随部署数据持久化，后续读取会隐藏其值；请保护数据库及备份。

保存后的配置对新项目生效。已有项目继续使用创建时绑定的配置版本，修改 Worker 或运行设置不会更新这些项目。

## 分离 Server 与 Dispatcher

先启动 Server：

```bash
aether serve --deployment /absolute/path/deployment.toml
```

在另一个终端或服务中启动 Dispatcher：

```bash
aether dispatch --deployment /absolute/path/deployment.toml
```

Server 管理 SQLite，不需要 Docker 权限。Dispatcher 管理调度和 Docker 访问，通过包含 `/api/v1` 的 `dispatcher.control_plane_url` 连接 Server。在另一台主机运行 Dispatcher 时，为其准备部署 ID 相同、Server URL 可达的配置文件；它不会打开 Server 的数据库路径。

`aether run` 合并两个角色，需要同时具备数据库和 Docker 访问权限。长期部署请用你的服务管理器托管相应命令；安装脚本不生成服务单元。

## 健康状态与数据持久化

```bash
curl -i http://127.0.0.1:8000/api/v1/health
curl -i http://127.0.0.1:8000/api/v1/ready
```

| 接口 | HTTP 响应 | 含义 |
| --- | --- | --- |
| `/health` | `200`，`status: ok` | HTTP 服务有响应，数据库可访问。 |
| `/ready` | `200`，`status: ready` | 已有生效配置和活跃 Dispatcher。 |
| `/ready` | `503`，`configuration_required` | 需要保存部署配置。 |
| `/ready` | `503`，`dispatcher_unavailable` | 需要启动或恢复 Dispatcher。 |
| 任一接口 | `503`，`database_unavailable` | 检查数据库可用性和 Server 日志。 |

就绪检查不会逐一检查模型提供方，也不保证每次 Worker 调用都能成功。提供方或运行环境问题可通过 Worker 编辑器和执行记录排查。

SQLite 保存项目状态、配置版本和凭据数据。简单的离线备份方式是先正常停止 Dispatcher 和 Server，再将整个数据目录及部署配置复制到受保护的位置。项目容器中的文件需要单独备份，复制数据库不包含容器文件系统。备份应存放在源码仓库之外。

## 验证

在仓库根目录执行后端检查：

```bash
go test ./cmd/... ./internal/... ./test/...
go vet ./cmd/... ./internal/... ./test/...
go test -race -timeout=60m ./cmd/... ./internal/... ./test/...
```

包含大量 SQLite 操作的竞态测试，单个包可能需要数十分钟。上述超时限制分别作用于每个包。前端检查：

```bash
cd web
npm ci
npm run build
npm run lint
npm run typecheck
npm test
```

真实 Docker 集成测试会创建容器，需要前文所述的 Worker 镜像。在仓库根目录执行：

```bash
AETHER_REQUIRE_DOCKER_TESTS=1 \
AETHER_DOCKER_TEST_IMAGE=ghcr.io/c1ph3rx13/aether-worker:latest \
go test -p 1 -tags dockerintegration ./internal/docker ./cmd/aether \
  -run DockerIntegration -count=1 -timeout=15m
```

手动构建原生程序时，先生成前端：

```bash
(cd web && npm ci && npm run build)
mkdir -p dist
CGO_ENABLED=0 go build -buildvcs=false -trimpath -tags embedstatic \
  -o dist/aether ./cmd/aether
./dist/aether --version
```

必须使用 `embedstatic` 构建标签才能包含生产界面。

## 常见问题

| 现象 | 检查内容 |
| --- | --- |
| 前端构建无法下载依赖或字体 | 是否能访问配置的包源及 Google Fonts。 |
| 找不到 `aether` 命令 | 安装目录是否加入 `PATH`，或改用二进制绝对路径。 |
| 部署配置校验失败 | 是否显式指定文件、仅使用支持的 TOML 字段、填写绝对数据库及 socket 路径。 |
| Docker 权限或连接错误 | daemon 是否运行，执行用户能否访问配置的 socket。 |
| 找不到 Worker 镜像 | 是否已在 Dispatcher 使用的 daemon 上拉取配置的镜像。 |
| 保存时提供方检查失败 | 端点、API 协议、模型、凭据和网络连通性。 |
| 无法创建项目 | 配置是否保存，是否覆盖 Bootstrap/Reason/Explore。 |
| 项目一直等待执行 | Dispatcher 是否可用，Worker 镜像、提供方连通性及容量配置是否正常。 |
| 部署身份不匹配 | 配置中的 ID 必须与数据库中已保存的身份一致。 |

卸载应用时删除已安装的二进制即可。部署数据库和 Docker 项目数据需要另行管理。
