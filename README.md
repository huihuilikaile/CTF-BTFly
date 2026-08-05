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

因为一些原因1.3.1以后不再开源，具体可以看最后，2.0及以后版本的需要一定门槛进群获取，请谅解🙇‍。

## 图文说明与更新记录

**使用时需要启动docker desktop，配置镜像文件(下文有教程)，配置env文件(模型baseurl+key+id等信息),打开右上角显示绿色提示灯,模型连接正常，即可开始使用。**

![image-20260802170230450](/template/image-1.png)

**docker镜像可以一键导入(2.0新增)**

![image-20260804202707879](/template/12.png)

**拖动题目附件到对应的区域会启动创建题目窗口**

![image-20260802170718611](/template/image-2.png)

**填入题目信息，选择已经配置的模型，web/pwn/...配置远程地址，flag格式默认是flag{...}**

![image-20260802170822399](/template/image-3.png)

**2.0版本新增多模型协同模式 针对ctf难题可以选择此模式**

![image-20260804202439143](/template/11.png)

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

**允许自定义主题配色(2.0新增)**

![image-20260804202849172](/template/13.png)

每道题的默认工作区结构：

```text
data/workspaces/task_xxx/
├── attachments/       用户上传的题目附件
├── artifacts/         Agent 保存的脚本、响应和证据
├── .pi-sessions/      Pi 会话数据
└── WRITEUP.md         中文可复现解题报告
```

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

### 专项镜像

**进群可以下载构建好的: 921416626**

## 题型与专项镜像

| 题型 | 镜像 | 代表工具 |
|---|---|---|
| Web | `ctf-agent-pi-web:0.1.0` | Nmap、SQLMap、Gobuster、WhatWeb |
| Crypto | `ctf-agent-pi-crypto:0.1.0` | John、gmpy2、PyCryptodome、SymPy、Z3 |
| Pwn | `ctf-agent-pi-pwn:0.1.0` | GDB、QEMU、Pwntools、Ropper、Checksec |
| Reverse | `ctf-agent-pi-reverse:0.1.0` | Apktool、angr、GDB、Strace、Ltrace |
| Forensics | `ctf-agent-pi-forensics:0.1.0` | Binwalk、Tshark、Yara、Sleuth Kit、Volatility |
| Misc | `ctf-agent-pi-misc:0.1.0` | FFmpeg、ImageMagick、Steghide、ZBar、SciPy |

## **赞助**

**感谢以下师傅对工具的认可，我会持续努力的更新！！！**

如有未统计上的师傅记得联系我🫡

**枫 二月犬 Skywalker ShyL0ck Archie_x hqn Douze 馬一强 御风 Albert L*o 听风 子鹿 魔天王 2rrr mycafday 秋雨渐冷. 爱弥斯**

------

如果你对工具或者agent感兴趣可以进群交流:921416626

## 最后

**CTF-BTFly是一个开源项目，这其中很多代码都是ai编写的，我只负责了构建框架和ui设计。
虽然是ai项目，但我一直觉得开源精神是互联网最伟大的精神，这个项目从今年4月份跟朋友开始构思，5月份初版做出来，后面进行了很多测试，效果不错，放了暑假我在初版的基础上更换了ui和后端agent，也重新设计了一下架构，本来没有准备放出来，但是效果确实不错，ui、解题速度、安全性都很可以，所以我开源在了github，只是希望能跟更多的大佬交流agent，同时也想有一个高star的项目(谁不想要一个高star的项目呢，在此感谢各位关注/点star的师傅，还有pi,gpt,ds)。
在开源的这个期间呢，我感受到了开源作者的一些痛苦，要面对很多问题，有一些问题我都无法诉说，更甚至的呢还要被说“招笑”，我也不知道这个项目好笑在哪里😂，可能是agent不是自己写的用的pi，可能项目代码是ai写的，我只负责了框架，基于以上呢，我决定不在github上放出来了，1.3.1版本也已经非常好用了，可以实现一定的自动化了。后面优化agent的也不开放了，毕竟是开源的，想要什么功能自己可以做，想弄的尽量早点存代码，我要删除了，群里也不再解决问题了。
最后：🙏🏻🙏🏻🙏🏻**

**欢迎进群交流agent:921416626**