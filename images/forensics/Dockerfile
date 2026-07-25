# Forensics 镜像继承公共 Pi 环境和跨方向资料库。
FROM ctf-agent-pi-base:0.1.0

# 安装文件雕刻、磁盘、网络、YARA、元数据和内存取证工具。
USER root
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        binwalk \
        foremost \
        libimage-exiftool-perl \
        p7zip-full \
        sleuthkit \
        tshark \
        yara \
    && rm -rf /var/lib/apt/lists/* \
    && python -m pip install --no-cache-dir \
        oletools==0.60.2 \
        scapy==2.6.1 \
        volatility3==2.26.2

# 将 Forensics 行为入口和详细参考资料合并到 Pi 的 forensics Skill 目录。
COPY --chown=ctf:ctf agents/forensics/ /home/ctf/.pi/agent/skills/forensics/
COPY --chown=ctf:ctf skills/forensics/ /home/ctf/.pi/agent/skills/forensics/

# 构建结束恢复非特权默认用户。
USER ctf
