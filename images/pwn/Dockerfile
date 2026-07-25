# Pwn 镜像继承公共 Pi 环境和跨方向资料库。
FROM ctf-agent-pi-base:0.1.0

# 安装编译链、GDB、QEMU、ELF 修补与常用漏洞利用 Python 工具。
USER root
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        binutils \
        build-essential \
        checksec \
        gdb \
        gdbserver \
        patchelf \
        qemu-user \
    && rm -rf /var/lib/apt/lists/* \
    && python -m pip install --no-cache-dir \
        pwntools==4.14.1 \
        ropper==1.13.13

# 将 Pwn 行为入口和详细参考资料合并到 Pi 的 pwn Skill 目录。
COPY --chown=ctf:ctf agents/pwn/ /home/ctf/.pi/agent/skills/pwn/
COPY --chown=ctf:ctf skills/pwn/ /home/ctf/.pi/agent/skills/pwn/

# 构建结束恢复非特权默认用户；运行时仅 Pwn 会额外获得 SYS_PTRACE。
USER ctf
