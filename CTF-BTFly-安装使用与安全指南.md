# CTF-BTFly 安装、使用及安全指南

> 适用对象：在**明确授权的 CTF 比赛、靶场、教学环境或本地练习题**中使用 CTF-BTFly 的个人或团队。  
> 本文对应本地优先的 CTF-BTFly 桌面版：Wails v3 桌面端 + 独立 Go daemon + Docker Pi Agent 沙箱。

## 1. CTF-BTFly 是什么

CTF-BTFly 是一个面向 CTF 解题流程的本地桌面工作台。每道题会创建独立工作区，并由对应题型的 Docker 沙箱运行 Pi Agent。桌面端用于提交题目、查看实时过程、管理附件、补充提示、暂停或恢复任务、查看文件和下载解题报告。

它的目标是降低重复操作成本，并保留完整的解题证据、脚本和 Writeup；它不是对任意目标进行自动攻击的工具，也不保证能够解决所有题目。

## 2. 架构概览

```text
CTF-BTFly.exe（Wails 桌面端）
        │ REST / WebSocket（仅本机）
        ▼
ctfagent-daemon.exe（Go 控制平面）
        │ Docker Engine SDK / Pi RPC JSONL
        ▼
按题创建的 Docker Agent 沙箱
├── Pi Agent
├── 当前题型工具链与 CTF Skills
├── /workspace/attachments（题目附件）
├── /workspace/artifacts（脚本与证据）
└── WRITEUP.md（解题报告）
        │
        ▼
模型网关（daemon 托管真实 API Key） → 模型服务商
```

基本原则：Agent 可以在沙箱内自主执行命令、读写工作区、安装解题依赖和访问授权目标；沙箱不应获得 Docker Socket、宿主机私钥、长期模型 API Key 或宿主机目录挂载权限。

## 3. 技术栈

| 模块 | 当前实现 |
|---|---|
| 桌面壳 | Wails v3 |
| 前端 | React、TypeScript、Vite |
| UI | Tailwind CSS、shadcn/ui 风格组件、Lucide 图标 |
| 后端 | 独立 Go daemon、REST API、WebSocket |
| 本地数据 | SQLite、任务工作区与 Artifact 文件目录 |
| 沙箱管理 | Docker Engine SDK、每题一个容器实例 |
| Agent 协议 | Pi Agent RPC（stdin/stdout JSONL） |
| 模型访问 | 本地 Model Gateway，向 Agent 签发任务级访问凭据 |
| 专项镜像 | Web、Crypto、Pwn、Reverse、Forensics、Misc |
| 知识与流程 | `skills/` 下的题型 Skill、参考资料和检查清单 |

普通题建议优先使用 gVisor；需要 `ptrace`、GDB、QEMU 或特殊系统调用的 Pwn/动态逆向题建议使用 Kata Containers 或专用虚拟机 Worker。开发环境在没有上述运行时时可能降级为受限的 `runc`，这不等价于生产级隔离。

## 4. 系统要求

### 推荐环境

- Windows 10/11 x64，Docker Desktop 已启动，建议使用 WSL2 后端；
- 或 Linux x64，Docker Engine 可用；
- 足够的磁盘空间：专项镜像和题目附件可能占用数十 GB；
- 建议至少 16 GB 内存。取证、逆向和大型附件建议 32 GB 以上；
- 可访问所选模型服务的网络，或部署可用的私有模型网关。

### 运行前检查

1. 启动 Docker Desktop，确认 Docker 状态正常。
2. 确保所有题目目标均属于比赛、靶场或你拥有书面授权的范围。
3. 确保本机没有把 Docker Socket、SSH 私钥、云凭据等敏感目录暴露给 CTF-BTFly 容器。
4. 预留足够磁盘空间，尤其是 PCAP、内存镜像、固件或大型压缩包。

## 5. 安装与首次配置

### 5.1 使用已构建程序

发行目录的核心文件为：

```text
bin/
├── CTF-BTFly.exe
└── ctfagent-daemon.exe
```

`CTF-BTFly.exe` 会启动或连接同目录的 `ctfagent-daemon.exe`。两个文件必须保留在同一目录中。

### 5.2 配置模型网关环境变量

在 `CTF-BTFly.exe` 同目录下创建 `.env` 文件：

```env
# 上游模型服务的 OpenAI 兼容或项目适配地址
CTF_UPSTREAM_MODEL_BASE_URL=https://your-model-gateway.example/v1

# 只由本机 daemon 使用；不要把此值写入题目附件、提示词或容器镜像
CTF_UPSTREAM_MODEL_API_KEY=replace-with-your-api-key

# 实际模型 ID，必须与上游服务可用模型一致
CTF_MODEL_ID=your-model-id

# 可选，默认 true：为 OpenAI 兼容的流式响应请求最终 usage 数据。
# 若上游明确报错不支持 stream_options，可改为 false。
CTF_MODEL_INCLUDE_STREAM_USAGE=true
```

示例文件路径：

```text
bin/.env
```

修改 `.env` 后，需要完全退出 CTF-BTFly 与 daemon，再重新启动 `CTF-BTFly.exe`。如果界面提示“尚未配置模型网关”，应先检查以上三个变量是否存在、没有多余引号，并确认上游地址和模型 ID 正确。

`CTF_MODEL_INCLUDE_STREAM_USAGE` 默认开启。它会让标准 OpenAI 兼容的 `/chat/completions` 流式请求在结束时返回 `usage`，用于统计输入、输出和总 Token；不会改变题目、Prompt 或真实 API Key。少数兼容服务若拒绝 `stream_options`，才需要设置为 `false`。

### 5.3 构建镜像

源代码目录下的镜像构建脚本会依次构建基础镜像和六个专项镜像：

```powershell
powershell -ExecutionPolicy Bypass -File .\images\build.ps1
```

生成的标签为：

```text
ctf-agent-pi-base:0.1.0
ctf-agent-pi-web:0.1.0
ctf-agent-pi-crypto:0.1.0
ctf-agent-pi-pwn:0.1.0
ctf-agent-pi-reverse:0.1.0
ctf-agent-pi-forensics:0.1.0
ctf-agent-pi-misc:0.1.0
```

所有镜像包含完整 CTF 资料库 `/opt/cpi/ctf-skills`；当前题型的详细 Skill 同时安装在 Pi 的标准 Skills 目录中。更新 `skills/` 后必须重新执行该构建脚本；已在运行的容器不会自动获得新文件。

### 5.4 从源码重新构建桌面程序

在项目根目录执行：

```powershell
# 后端单元测试
go test ./internal/...

# 重新构建独立 daemon
wails3 task daemon:build

# 构建 Wails 桌面程序
wails3 build
```

构建结束后，从 `bin/CTF-BTFly.exe` 启动最新版本。

## 6. 完整使用流程

### 6.1 启动与环境确认

1. 启动 Docker Desktop。
2. 启动 `CTF-BTFly.exe`。
3. 在“系统概况”确认“后端”和“Docker”状态均正常。
4. 查看“已启用容器”面板，确认没有遗留的不需要实例；已完成题目应进入题目页关闭实例以释放内存。

### 6.2 创建题目

点击“新建题目”，填写：

- **题目名称**：便于后续检索；
- **题目方向**：Web、Crypto、Pwn、逆向、取证或杂项；
- **题目描述**：原始题面、已知信息、限制条件；
- **授权目标**：仅填写赛事明确授权的 URL、IP、端口或本地靶机地址；
- **预期 Flag 格式**：可选。`flag{...}` 仅为参考，最终以比赛规则和实际结果为准；
- **题目附件**：可选择文件、文件夹，或将其拖放到附件区域。

附件会复制到任务工作区的 `/workspace/attachments`，新启动的 Agent 可直接读取。不要把个人密码、私钥、云凭据、企业数据或未获授权的样本上传进去。

### 6.3 启动解题

在题目页面点击“启动 Pi Agent”。CTF-BTFly 将：

1. 为题目创建独立工作区；
2. 选择与题型对应的镜像和运行时；
3. 启动 Pi RPC；
4. 生成带有题目范围、授权边界、Skill 路径与报告要求的系统提示词；
5. 将实时事件、工具输出与重要发现显示在“解题过程”中。

“解题过程”标签栏会显示当前已启用容器的镜像和短容器 ID。过程区会保留滚动位置；多次重新尝试会形成不同的尝试标签。

### 6.4 查看过程、终端、文件和报告

| 页面 | 用途 |
|---|---|
| 提示词 | 查看只读系统策略，编辑当前题目的补充提示 |
| 解题过程 | 查看重要 Agent、工具、沙箱与交接事件 |
| 终端 | 查看工具调用及其输出摘要 |
| 文件 | 浏览、预览和下载题目附件、脚本、Artifact 与报告文件 |
| 解题报告 | 查看并下载 `WRITEUP.md` |

Agent 应将有效脚本保存到 `/workspace/artifacts`，并将最终报告写入 `WRITEUP.md`。报告中的 `## 最终 Flag` 段落是 CTF-BTFly 识别最终 Flag 的主要依据；不要把终端中随机出现的字符串直接当作已验证 Flag。

### 6.5 暂停、补充信息与恢复

任务运行中可选择：

- **暂停**：中断当前 Pi 回合，但保留 Docker 容器、Pi 会话、上下文、工作区与已有产物；
- **补充提示**：暂停后在“提示词”页填写新线索、已知参数、人工分析结论或新的验证要求；
- **保存并恢复解题**：将补充信息作为继续消息发送到同一 Pi 会话；
- **中止**：停止当前任务。中止后可补充提示并使用“重新尝试”创建新的解题实例。

暂停不是对 Linux 进程做无限期冻结：当前 Pi 操作会被取消，因此正在执行的工具命令可能中断。恢复会继续同一会话。若暂停期间 daemon 被完全退出，原 Pi 会话可能无法恢复；此时应保留补充提示并选择“重新尝试”。

### 6.6 杂项题与 Crypto 专项交接

Misc Agent 遇到核心密码学阻塞时，可以保存参数、样本与当前发现，并请求独立 Crypto 子任务分析。系统会：

1. 关闭等待中的 Misc 容器，减少同时占用的内存；
2. 创建隔离的 Crypto 子容器；
3. 仅复制必要附件与 Artifact；
4. 将专项脚本和报告回写到父题目的 `artifacts/handoffs/`；
5. 自动恢复 Misc Agent 继续完成原题。

这不是通用无限多 Agent 调度。子容器仍应仅处理授权题目范围内的问题。

### 6.7 完成、复盘与释放资源

题目结束后：

1. 在“解题报告”核对关键步骤、脚本和最终 Flag；
2. 下载 WP 和关键 Artifact；
3. 如不再需要交互式环境，点击“关闭实例”；
4. 关闭实例只释放容器和模型会话，题目、附件、事件和报告会保留；
5. 对不再需要的已结束题解，可在左侧任务卡片右键删除。删除会移除工作区、附件、报告与事件记录，无法恢复。

### 6.8 查看模型用量

左侧点击“模型用量”可以查看本机 daemon 自新版本启动以来记录的模型调用情况：

- **总览**：输入 Token、输出 Token、总 Token 与请求数；
- **按日期 Token 用量**：最近 30 天的柱状图，鼠标悬停可查看当天输入、输出、总量与请求数；
- **按题目统计**：每道题目的输入、输出、总 Token、请求数和实际模型名称；
- **专项交接**：Misc 调用 Crypto 子任务时，子任务用量会合并显示在原 Misc 题目下。

统计由本地模型网关在上游响应完成后写入 `data/platform.db`。它不保存 Prompt、模型回复、附件、真实 API Key 或完整请求头。只有模型服务商返回 `usage` 时，Token 才会计入；未返回 `usage` 的调用仍会显示在请求数中，但不会被估算为不准确的 Token 数。旧版本运行过的历史题目不会自动补算。

## 7. CTF Skills 使用说明

`skills/` 目录保存各题型的 `SKILL.md`、参考资料与检查清单。当前约定如下：

| 题型 | 主要 Skill |
|---|---|
| Web | `skills/web/SKILL.md` |
| Crypto | `skills/crypto/SKILL.md` |
| Pwn | `skills/pwn/SKILL.md` |
| 逆向 | `skills/reverse/SKILL.md` |
| 取证 | `skills/forensics/SKILL.md` |
| 杂项 | `skills/misc/SKILL.md` |

新增或修改 Skills 时应：

1. 保持 `SKILL.md` 和相对引用路径有效；
2. 对引用的公开资料记录来源、许可证与适用范围；
3. 避免在 Skill 中写入真实凭据、内部目标或未授权攻击指令；
4. 重新构建镜像；
5. 使用本地历史题或靶场进行回归测试，再用于正式比赛。

## 8. 安全事项

### 8.1 必须遵守的隔离边界

即使 Agent 在容器中拥有较高权限，也**不得**向容器提供：

- `/var/run/docker.sock`；
- `--privileged`、`CAP_SYS_ADMIN`、`--cap-add ALL`；
- 宿主机根目录、用户目录、SSH 密钥目录或浏览器配置目录；
- 生产数据库凭据、云访问密钥、Kubernetes 管理 Token；
- 长期有效的模型 API Key；
- 未经筛选的企业内网或公网访问能力。

Pi 只能通过 daemon 的任务级访问凭据调用模型服务；真实上游 API Key 仅保存在 daemon 配置中。

### 8.2 网络边界

- 主动扫描、请求重放、漏洞验证和利用，仅限题面或比赛规则明确授权的目标；
- 可以按需从官方包仓库、项目发布页下载库与工具；
- 可以被动查阅公开 CTF 题源、历史赛题、官方页面和公开 Writeup；
- 软件源和公开参考网站不是攻击目标，禁止对其扫描、枚举、漏洞测试、凭据收集或访问非公开资源；
- 赛事规则比本指南优先。若比赛禁止联网参考或自动化操作，必须遵守该规则。

### 8.3 不可信输入与提示注入

题面、附件、网页内容、终端输出和反编译字符串均可能包含误导性文本。它们不能改变系统授权边界。应始终：

- 将题目文件视为不可信数据；
- 不执行附件中要求窃取环境变量、上传凭据或访问非授权主机的指令；
- 不将敏感文件路径、Token、密码或内部 URL 写入 Agent 提示词；
- 对 Agent 下载的脚本、二进制或依赖保持审慎，优先使用官方来源和可校验版本；
- 对最终 Flag、脚本和 Writeup 进行人工复核。

### 8.4 资源与数据管理

- 取证、密码破解和模糊测试可能消耗大量 CPU、内存、磁盘和 Token；
- 对每个任务设置可接受的运行时长、磁盘空间和模型预算；
- 完成后关闭不需要的沙箱实例；
- 对敏感比赛题、WP 和附件使用受控备份与访问权限；
- 遇到异常容器行为时，立即中止任务、关闭实例、保留事件日志并轮换相关密钥。

## 9. 合法使用声明

使用 CTF-BTFly 即表示你确认并承诺：

1. 仅在你拥有所有必要授权的 CTF、靶场、教学、测试或本地环境中使用；
2. 不将 CTF-BTFly 用于未经授权的扫描、入侵、绕过认证、数据窃取、拒绝服务、恶意代码传播或横向移动；
3. 不使用 Agent 自动化能力规避比赛规则、平台规则、服务条款或法律法规；
4. 对由你提交的目标、附件、提示词、模型配置和 Agent 操作承担全部责任；
5. 在团队或企业环境中，先取得管理者、资产所有者和比赛主办方的授权；
6. 遵守适用的网络安全、数据保护、知识产权、出口管制和竞赛规则。

如果你无法确认目标是否被授权，必须停止操作并向资产所有者或主办方确认。

## 10. 免责声明

CTF-BTFly 按“现状”和“可用性”提供，不提供任何明示或默示担保，包括但不限于：适销性、特定用途适用性、无错误运行、持续可用性、结果准确性或解题成功率。

使用者应自行评估 Agent 生成的命令、脚本、模型输出、Flag、Writeup 和第三方资料。CTF-BTFly 的自动化能力不代表其生成的行为天然安全、合法或符合比赛规则。

在适用法律允许的最大范围内，CTF-BTFly 的开发者、维护者和贡献者不对因使用、无法使用或误用本软件造成的直接、间接、附带、特殊、惩罚性或后果性损失承担责任，包括数据丢失、服务中断、比赛资格损失、模型费用、系统损坏、法律责任或第三方索赔。

## 11. 常见问题

### 提示“尚未配置模型网关”

检查 `bin/.env` 是否存在，并确认 `CTF_UPSTREAM_MODEL_BASE_URL`、`CTF_UPSTREAM_MODEL_API_KEY`、`CTF_MODEL_ID` 已正确设置。完全退出并重启 CTF-BTFly 后再试。

### Docker 正常但无法启动题目

确认对应 `ctf-agent-pi-<category>:0.1.0` 镜像存在。若不存在或更新了 Skills，请运行 `images/build.ps1` 重建镜像。

### 暂停后无法恢复

确认 daemon 没有在暂停期间退出。若界面提示原会话不可恢复，保存补充提示后使用“重新尝试”；工作区附件和已有 Artifact 会被保留。

### 为什么不直接让容器使用 `--privileged`

`--privileged`、Docker Socket 或宿主机目录挂载会显著扩大容器逃逸和密钥泄露风险。CTF Agent 应在可销毁的沙箱内自主，而不是获得宿主机管理权限。

### 为什么最终 Flag 没有自动显示

CTF-BTFly 优先读取报告中明确的 `## 最终 Flag` 段落，避免把工具输出中的随机字符串误判为 Flag。请检查 `WRITEUP.md` 是否按要求写入最终结果，并以赛事平台验证为准。

### 模型用量页只有请求数，没有 Token

这表示模型服务商没有在响应中提供标准 `usage` 字段。对于 OpenAI 兼容流式接口，先确认 `.env` 中没有将 `CTF_MODEL_INCLUDE_STREAM_USAGE` 设为 `false`，然后完全退出 CTF-BTFly 与 daemon 后重新启动。如果上游仍不返回 usage，CTF-BTFly 会保留请求次数而不显示猜测值；可联系模型服务商确认其 OpenAI 兼容接口是否支持 `stream_options.include_usage`。
