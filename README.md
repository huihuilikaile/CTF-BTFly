# CTF-BTFly
图文说明:
https://mp.weixin.qq.com/s/RLU-ROZ0YfjJMzR3BDdl8g

本地优先的自主 CTF 解题工作台。

```text
Wails v3 + React 19 + Tailwind CSS 4
                │ REST / WebSocket
        Independent Go 1.26 daemon
      SQLite · Docker SDK · model gateway
                │ Docker attach JSONL
        Pi Agent RPC sandbox per task
```

GUI 只负责操作、日志与工作台；独立 daemon 持有 Docker 权限、SQLite、短期模型凭证和 Pi 会话。每个题目都会创建一个新的容器实例，容器完成后可保留作人工接管或由 daemon 销毁。

## 已实现

- Wails v3 桌面工作台：任务创建、系统状态、实时事件时间线、Flag 候选、Sandbox 信息；
- 独立 Go daemon：本地 REST/WebSocket、任务鉴权、SQLite 事件日志和断线重放；
- Docker SDK：按题型选择 Pi 镜像、资源限额、`SYS_PTRACE` 仅给 Pwn、`gVisor → runc` / `Kata → runc` 运行时探测回退；
- Pi RPC：JSONL stdin/stdout，标准化 Agent/工具/错误事件；
- 模型网关：真实 API Key 留在 daemon，容器只获得任务级临时 token；按题目持久化输入、输出与总 Token，并展示模型名称与按日期统计；
- 专项 Pi 镜像：Web、Crypto、Pwn、Reverse、Forensics、Misc；
- Artifact 工作目录：每道题独立挂载到容器 `/workspace`。

## 前置条件

- Go 1.26（项目会使用 Go toolchain 自动下载）；
- Node.js 24+ 与 npm；
- Docker Desktop / Docker Engine；
- Wails v3 CLI；
- 可用的 OpenAI-compatible 模型接口。

Windows 开发环境可直接使用 Docker Desktop。当前 Docker Desktop 未安装 `runsc`/Kata 时，daemon 会明确显示 `runc` 开发回退警告；线上或处理不可信二进制时，应使用 Linux Worker 上的 gVisor（普通题）或 Kata/VM（Pwn）。

## 配置模型网关

daemon 会自动读取与 `CTF-BTFly.exe` 同目录的 `.env` 中的真实密钥；不要把它放进 Docker 镜像、题目工作目录或前端代码。发布版默认位置为 `bin\\.env`。也可以用 `CTF_AGENT_ENV_FILE` 指定一个绝对路径。

请在 `CTF-BTFly.exe` 所在目录创建被 Git 忽略的 `.env`。填写以下三项后，完全退出并重启 GUI（它会重新启动 daemon）：

```powershell
CTF_UPSTREAM_MODEL_BASE_URL=https://your-openai-compatible-endpoint/v1
CTF_UPSTREAM_MODEL_API_KEY=your-real-provider-key
CTF_MODEL_ID=your-model-id
# 可选：默认 true。若上游 OpenAI 兼容服务拒绝 stream_options，可设为 false。
CTF_MODEL_INCLUDE_STREAM_USAGE=true
```

系统环境变量仍优先于 `.env`，适合 CI 或临时覆盖配置。

可选变量：

```text
CTF_AGENT_DATA_DIR    # 可选：覆盖默认数据目录；默认在 CTF-BTFly.exe 同目录的 data/ 文件夹
CTF_DAEMON_ADDRESS    # 默认 127.0.0.1:17321
CTF_DAEMON_TOKEN      # 默认启动时安全生成并保存在本地
CTF_MODEL_INCLUDE_STREAM_USAGE # 默认 true；请求上游在流式结束时返回 usage
```

daemon 会为每个任务签发短期 token，容器通过 `http://host.docker.internal:<port>/model` 访问模型网关。真实 Provider Key 不会进入 Pi 容器。模型网关只保存请求归属、模型名、Token 数、响应状态与耗时；不会保存 Prompt、模型回复或密钥。

## 模型用量

“模型用量”页会显示总输入、输出、总 Token、请求数、最近 30 天的用量柱状图，以及每道题使用的模型和 Token 汇总。Misc → Crypto 专项交接产生的子任务用量会归并至原题。

Token 数据从上游模型响应的 `usage` 字段获得。对于标准 OpenAI 兼容流式接口，CTF-BTFly 默认自动补充 `stream_options.include_usage=true`。旧任务不会补算；只有新版本 daemon 启动后的模型请求会写入本地 SQLite 账本。若上游不提供 `usage`，CTF-BTFly 仅记录请求次数而不会猜测 Token。

## 构建与启动

在项目根目录执行：

```powershell
# 1. 构建 Pi 专项镜像（首次或 Dockerfile 修改后）
./images/build.ps1 -Version 0.1.0

# 2. 构建独立控制平面
wails3 task daemon:build

# 3. 开发模式（会先构建 daemon）
wails3 task dev

# 或构建桌面程序
wails3 build
```

产物：

```text
bin/CTF-BTFly.exe          # Wails GUI
bin/ctfagent-daemon.exe   # 独立 Control Plane
```

GUI 启动时会连接已有 daemon；若没有运行，则自动启动同目录的 `ctfagent-daemon.exe`。也可以直接运行：

```powershell
wails3 task daemon:run
```

## 镜像与权限模型

| 题型      | 镜像                           | 容器内权限          | 目标运行时  |
| --------- | ------------------------------ | ------------------- | ----------- |
| Web       | `ctf-agent-pi-web:0.1.0`       | root in sandbox     | gVisor      |
| Crypto    | `ctf-agent-pi-crypto:0.1.0`    | root in sandbox     | gVisor      |
| Reverse   | `ctf-agent-pi-reverse:0.1.0`   | root in sandbox     | gVisor/Kata |
| Pwn       | `ctf-agent-pi-pwn:0.1.0`       | root + `SYS_PTRACE` | Kata/VM     |
| Forensics | `ctf-agent-pi-forensics:0.1.0` | root in sandbox     | gVisor/Kata |
| Misc      | `ctf-agent-pi-misc:0.1.0`      | root in sandbox     | gVisor      |

Agent 在沙箱内可以自由执行命令、写脚本和安装工具；但平台不会提供 `--privileged`、Docker Socket、宿主机目录、长期 API Key 或任意宿主机密钥。网络白名单/egress proxy 是下一阶段部署到 Linux Worker 时必须启用的边界。

## 验证

```powershell
go test ./...
cd frontend; npm run build
wails3 build
```

当前自动化验证覆盖 SQLite 的单调事件序列与重放、模型网关短期 token 替换与 Token 用量解析、题目/子任务用量汇总、HTTP 创建任务/鉴权/事件回放、Pi 事件标准化，以及桌面与前端生产构建。

更多镜像说明见 [images/README.md](images/README.md)。