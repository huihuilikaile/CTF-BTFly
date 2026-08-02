#                              CTF-BTFLY: CTF agent 自动化解题工具



> [!CAUTION]
> CTF-BTFly 只能用于明确授权的 CTF 题目、靶场和安全研究环境。  
> 请勿使用本项目扫描、测试或攻击未授权目标，也不要把比赛附件、Flag、凭据或私有数据上传到第三方服务。

## 项目简介

CTF-BTFly 将 GUI 与高权限控制平面分离：

- **Wails + React 桌面端**负责题目管理、状态展示、事件时间线、文件预览、Writeup 和模型用量；
- **独立 Go daemon**负责 SQLite、Docker、任务状态机、模型凭据和 Agent 生命周期；
- **每题一个 Docker 沙箱**，根据题型加载 Web、Crypto、Pwn、Reverse、Forensics 或 Misc 专项工具；
- **Pi RPC Agent**在容器内自主分析附件、执行命令、编写脚本并生成中文 `WRITEUP.md`；
- **本地模型网关**把题目级短期 Token 替换为真实上游 Key，真实密钥不会进入容器或前端。

任务事件会先写入 SQLite，再通过 WebSocket 实时推送。前端断线或切换页面后，可以根据单调递增的 `sequence` 补齐历史。

## 图文说明与更新记录

**使用时需要启动docker desktop，配置镜像文件(下文有教程)，配置env文件(模型baseurl+key+id等信息),打开右上角显示绿色提示灯,模型连接正常，即可开始使用。**

![image-20260802170230450](/template/image-1.png)

**拖动题目附件到对应的区域会启动创建题目窗口**

![image-20260802170718611](/template/image-2.png)

**填入题目信息，选择已经配置的模型，web/pwn/...配置远程地址，flag格式默认是flag{...}**

![image-20260802170822399](/template/image-3.png)

**题目创建好启动agent即可**

![image-20260802171115701](/template/image-4.png)

**提示词 可以在这里暂停增加新的提示**

![image-20260802171232549](/template/image-5.png)

**解题过程 显示ai执行的一系列操作和思考过程 右上角可以暂停和中止 显示解题时间**

![image-20260802171400156](/template/image-6.png)

**终端和文件会显示工具执行的信息，产生的文件，文件允许下载**

![image-20260802171615345](/template/image-7.png)

**wp 解题成功后会自动编写wp 允许下载**

![image-20260802171904333](/template/image-9.png)

**题目卡片可以删除题目 可以选择是否保存wp**

![image-20260802172032323](/template/image-10.png)

每道题的默认工作区结构：

```text
data/workspaces/task_xxx/
├── attachments/       用户上传的题目附件
├── artifacts/         Agent 保存的脚本、响应和证据
├── .pi-sessions/      Pi 会话数据
└── WRITEUP.md         中文可复现解题报告
```

项目图文说明和版本更新记录同时发布于：

- [CTF-BTFly 图文说明（一）](https://mp.weixin.qq.com/s/RLU-ROZ0YfjJMzR3BDdl8g)
- [CTF-BTFly 图文说明（二）](https://mp.weixin.qq.com/s/_bZ32TZykNCsyjqdLdRVXw)
- [CTF-BTFly 图文说明（三）](https://mp.weixin.qq.com/s/9Tznr2-ZnFP9knj2J3CqFg)

## 快速开始

### 配置模型网关

在最终 `CTF-BTFly.exe` 所在目录创建 `.env`。开发构建默认产物位于 `bin/`，因此通常创建 `bin/.env`：

```env
CTF_UPSTREAM_MODEL_BASE_URL=https://your-openai-compatible-endpoint/v1
CTF_UPSTREAM_MODEL_API_KEY=your-real-provider-key
CTF_MODEL_ID=your-model-id
CTF_MODEL_INCLUDE_STREAM_USAGE=true
CTF_MODEL_SUPPORTS_IMAGES=false
# 多模型（可选）：设置 CTF_MODELS 后会优先使用以下配置，旧单模型变量仍兼容。
CTF_MODELS=deepseek,vision
CTF_DEFAULT_MODEL=deepseek
CTF_MODEL_DEEPSEEK_BASE_URL=https://api.deepseek.com/v1
CTF_MODEL_DEEPSEEK_API_KEY=your-deepseek-key
CTF_MODEL_DEEPSEEK_ID=deepseek-chat
CTF_MODEL_DEEPSEEK_SUPPORTS_IMAGES=false
CTF_MODEL_VISION_BASE_URL=https://your-vision-endpoint/v1
CTF_MODEL_VISION_API_KEY=your-vision-key
CTF_MODEL_VISION_ID=your-vision-model
CTF_MODEL_VISION_SUPPORTS_IMAGES=true

```

> [!IMPORTANT]
> `.env` 包含真实模型密钥，不要提交到 Git、复制进 Docker 镜像或放入题目工作区。

桌面端可在“系统概况 → 模型连接 → 管理模型”中新建或编辑多个配置；界面只显示 URL、模型 ID、能力开关和“密钥已设置”状态，绝不会回显 API Key。保存或“重新读取并检测”会原子热更新模型池，新配置会立即出现在检测模型和新建题目下拉框中，运行中题目仍保留原连接和短期 Token，无需重启 daemon/桌面程序。

已保存模型可通过右键菜单删除。删除操作需要模态确认；后端会独立检查所有未结束题目，如果仍有题目使用该模型则返回冲突并保留配置。删除 `.env` 中的模型同时会移除对应 API Key，因此该操作不可撤销。

### 构建专项镜像

windows下下载docker desktop

```powershell
.\images\build.ps1 -Version 0.1.0
```

首次构建需要下载 Node、Python、Debian 软件包和各题型工具，耗时取决于网络与 Docker 缓存。

### 桌面程序

主要：

```text
bin/
├── CTF-BTFly.exe
└── ctfagent-daemon.exe
```

GUI 启动时会优先连接已有 daemon；未检测到可用实例时，会自动启动同目录的 `ctfagent-daemon.exe`。

## 模型网关配置

| 环境变量 | 必填 | 默认值 | 作用 |
|---|---:|---|---|
| `CTF_UPSTREAM_MODEL_BASE_URL` | 是 | — | OpenAI-compatible API 基础地址 |
| `CTF_UPSTREAM_MODEL_API_KEY` | 是 | — | 真实上游 API Key，仅 daemon 持有 |
| `CTF_MODEL_ID` | 是 | — | Agent 使用的模型 ID |
| `CTF_MODEL_INCLUDE_STREAM_USAGE` | 否 | `true` | 为流式请求加入 `stream_options.include_usage` |
| `CTF_MODEL_SUPPORTS_IMAGES` | 否 | `false` | 仅在上游明确兼容 OpenAI `image_url` 内容块时设为 `true`；DeepSeek 应保持 `false` |
| `CTF_AGENT_ENV_FILE` | 否 | 程序同目录 `.env` | 显式指定 daemon 配置文件 |
| `CTF_AGENT_DATA_DIR` | 否 | 程序同目录 `data/` | 覆盖 SQLite、日志和工作区目录 |
| `CTF_DAEMON_ADDRESS` | 否 | `127.0.0.1:18731` | daemon 监听地址 |
| `CTF_DAEMON_TOKEN` | 否 | 自动安全生成 | 覆盖本地控制平面 Token |
| `CTF_DAEMON_EXECUTABLE` | 否 | 自动查找 | GUI 启动的 daemon 路径 |
| `DOCKER_HOST` | 否 | Docker SDK 默认 | 指定 Docker Engine |

daemon 会为每次任务启动签发随机短期 Token。容器通过：

```text
http://host.docker.internal:<daemon-port>/model
```

访问本地模型网关。SQLite 用量账本只保存模型名、Token 数、状态码和耗时，不保存 Prompt、回复、请求头或真实 Key。

## 沙箱与安全模型

| 控制项 | 当前策略 |
|---|---|
| 默认内存 | 4 GiB |
| 默认 CPU | 4 核配额 |
| 默认 PID | 512 |
| Linux capabilities | 默认全部移除 |
| Pwn 额外能力 | `SYS_PTRACE` |
| `no-new-privileges` | 启用 |
| Docker Socket | 不挂载 |
| 宿主机目录 | 只挂载当前题目工作区 |
| 模型凭据 | 容器只获得任务级短期 Token |
| 普通题运行时 | 优先 gVisor，开发环境可降级 runc |
| Pwn 运行时 | 优先 Kata，开发环境可降级 runc |

> [!WARNING]
> 当前实现仍使用 Docker bridge 网络，尚未在网络层强制目标白名单。  
> `runc` 降级模式只适合本地开发和可信题目；不要把它视为针对恶意二进制的强隔离边界。

## 题型与专项镜像

| 题型 | 镜像 | 代表工具 | 目标运行时 |
|---|---|---|---|
| Web | `ctf-agent-pi-web:0.1.0` | Nmap、SQLMap、Gobuster、WhatWeb | gVisor |
| Crypto | `ctf-agent-pi-crypto:0.1.0` | John、gmpy2、PyCryptodome、SymPy、Z3 | gVisor |
| Pwn | `ctf-agent-pi-pwn:0.1.0` | GDB、QEMU、Pwntools、Ropper、Checksec | Kata/VM |
| Reverse | `ctf-agent-pi-reverse:0.1.0` | Apktool、angr、GDB、Strace、Ltrace | gVisor/Kata |
| Forensics | `ctf-agent-pi-forensics:0.1.0` | Binwalk、Tshark、Yara、Sleuth Kit、Volatility | gVisor/Kata |
| Misc | `ctf-agent-pi-misc:0.1.0` | FFmpeg、ImageMagick、Steghide、ZBar、SciPy | gVisor |

镜像详细说明见 [`images/README.md`](./images/README.md)。

## 最后

**1.3.1为最新版，因研究agent没有时间，此后一段时间不再更新/修复bug，优化agent的版本将第一时间在交流群和公众号发布说明。**

欢迎交流agent:921416626