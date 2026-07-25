# Web 镜像继承公共 Pi 环境和跨方向资料库。
FROM ctf-agent-pi-base:0.1.0

# 以 root 安装侦察、目录枚举、SQL 注入和 HTTP/JWT 分析工具。
USER root
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        dirb \
        dnsutils \
        gobuster \
        nmap \
        sqlmap \
        whatweb \
    && rm -rf /var/lib/apt/lists/* \
    && python -m pip install --no-cache-dir \
        beautifulsoup4==4.13.4 \
        httpx==0.28.1 \
        pyjwt==2.10.1 \
        requests==2.32.4

# 将 Web 行为入口和详细参考资料合并到 Pi 的 web Skill 目录。
COPY --chown=ctf:ctf agents/web/ /home/ctf/.pi/agent/skills/web/
COPY --chown=ctf:ctf skills/web/ /home/ctf/.pi/agent/skills/web/

# 构建结束恢复非特权默认用户。
USER ctf
