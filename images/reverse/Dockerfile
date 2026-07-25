# Reverse 镜像继承公共 Pi 环境和跨方向资料库。
FROM ctf-agent-pi-base:0.1.0

# 安装 APK/JVM、二进制检查、系统调用追踪、调试器和 angr 符号执行。
USER root
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        apktool \
        binutils \
        default-jre-headless \
        gdb \
        ltrace \
        strace \
    && rm -rf /var/lib/apt/lists/* \
    && python -m pip install --no-cache-dir \
        angr==9.2.170

# 将 Reverse 行为入口和详细参考资料合并到 Pi 的 reverse Skill 目录。
COPY --chown=ctf:ctf agents/reverse/ /home/ctf/.pi/agent/skills/reverse/
COPY --chown=ctf:ctf skills/reverse/ /home/ctf/.pi/agent/skills/reverse/

# 构建结束恢复非特权默认用户。
USER ctf
