# 第一阶段仅提供 Node 24 运行时，最终镜像仍以 Python 3.12 slim 为基础。
FROM node:24-bookworm-slim AS node-runtime

FROM python:3.12-slim-bookworm

# Pi 版本固定以保证 RPC 协议稳定；APT 镜像允许构建时覆盖。
ARG PI_VERSION=0.81.1
ARG DEBIAN_FRONTEND=noninteractive
ARG APT_MIRROR=https://mirrors.tuna.tsinghua.edu.cn/debian
ARG APT_SECURITY_MIRROR=https://mirrors.tuna.tsinghua.edu.cn/debian-security

# 从 Node 官方镜像复制二进制和全局模块，避免在 Python 镜像中重复安装 Node。
COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/node
COPY --from=node-runtime /usr/local/lib/node_modules /usr/local/lib/node_modules

# 切换 APT 源，安装通用命令行工具、Pi Agent，并创建非特权 ctf 用户及工作目录。
RUN sed -i \
        -e "s|http://deb.debian.org/debian|${APT_MIRROR}|g" \
        -e "s|http://deb.debian.org/debian-security|${APT_SECURITY_MIRROR}|g" \
        -e "s|http://security.debian.org/debian-security|${APT_SECURITY_MIRROR}|g" \
        /etc/apt/sources.list.d/debian.sources \
    && apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        bash \
        ca-certificates \
        coreutils \
        curl \
        file \
        findutils \
        git \
        jq \
        less \
        netcat-openbsd \
        procps \
        ripgrep \
        tini \
        unzip \
        wget \
        xz-utils \
    && rm -rf /var/lib/apt/lists/* \
    && ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
    && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
    && npm install -g --ignore-scripts "@earendil-works/pi-coding-agent@${PI_VERSION}" \
    && npm cache clean --force \
    && groupadd --gid 1000 ctf \
    && useradd --uid 1000 --gid ctf --create-home --shell /bin/bash ctf \
    && mkdir -p /workspace /home/ctf/.pi/agent/extensions /home/ctf/.pi/agent/skills \
    && chown -R ctf:ctf /workspace /home/ctf

# 公共规则、模型 Provider 与通用工作流放入 Pi 的标准配置目录。
COPY --chown=ctf:ctf agents/common/AGENTS.md /home/ctf/.pi/agent/AGENTS.md
COPY --chown=ctf:ctf agents/common/extensions/ /home/ctf/.pi/agent/extensions/
COPY --chown=ctf:ctf agents/common/skills/ /home/ctf/.pi/agent/skills/
# 每个镜像保留完整 CTF 资料库；专项 Dockerfile 再把本方向 SKILL.md
# 暴露到 Pi 标准目录，跨方向参考则继续从 /opt/cpi/ctf-skills 按需读取。
COPY --chown=ctf:ctf skills/ /opt/cpi/ctf-skills/

# 所有缓存与配置归属 ctf 用户，Python 输出即时刷新到 daemon 事件流。
ENV HOME=/home/ctf \
    XDG_CACHE_HOME=/home/ctf/.cache \
    XDG_CONFIG_HOME=/home/ctf/.config \
    XDG_DATA_HOME=/home/ctf/.local/share \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

# 镜像默认以非特权用户运行；daemon 创建容器时会根据平台策略显式指定用户。
WORKDIR /workspace
USER ctf

# tini 负责转发信号并回收孤儿进程，默认命令以 JSONL RPC 模式运行 Pi。
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["pi", "--mode", "rpc", "--session-dir", "/workspace/.pi-sessions"]
