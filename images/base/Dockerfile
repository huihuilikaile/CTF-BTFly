FROM node:24-bookworm-slim AS node-runtime

FROM python:3.12-slim-bookworm

ARG PI_VERSION=0.81.1
ARG DEBIAN_FRONTEND=noninteractive
ARG APT_MIRROR=https://mirrors.tuna.tsinghua.edu.cn/debian
ARG APT_SECURITY_MIRROR=https://mirrors.tuna.tsinghua.edu.cn/debian-security

COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/node
COPY --from=node-runtime /usr/local/lib/node_modules /usr/local/lib/node_modules

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

COPY --chown=ctf:ctf agents/common/AGENTS.md /home/ctf/.pi/agent/AGENTS.md
COPY --chown=ctf:ctf agents/common/extensions/ /home/ctf/.pi/agent/extensions/
COPY --chown=ctf:ctf agents/common/skills/ /home/ctf/.pi/agent/skills/
# Keep the complete CTF reference library in every image. Direction-specific
# Dockerfiles below expose their detailed SKILL.md under Pi's standard skill
# directory, while this copy keeps all cross-domain references available.
COPY --chown=ctf:ctf skills/ /opt/cpi/ctf-skills/

ENV HOME=/home/ctf \
    XDG_CACHE_HOME=/home/ctf/.cache \
    XDG_CONFIG_HOME=/home/ctf/.config \
    XDG_DATA_HOME=/home/ctf/.local/share \
    PYTHONUNBUFFERED=1 \
    PIP_DISABLE_PIP_VERSION_CHECK=1

WORKDIR /workspace
USER ctf

ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["pi", "--mode", "rpc", "--session-dir", "/workspace/.pi-sessions"]
