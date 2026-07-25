# Misc 镜像继承公共 Pi 环境和跨方向资料库。
FROM ctf-agent-pi-base:0.1.0

# 安装音视频、图像、二维码、隐写和科学计算工具。
USER root
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        ffmpeg \
        imagemagick \
        libzbar0 \
        qrencode \
        sox \
        steghide \
        zbar-tools \
    && rm -rf /var/lib/apt/lists/* \
    && python -m pip install --no-cache-dir \
        numpy==2.3.1 \
        pillow==11.3.0 \
        scipy==1.16.0

# 将 Misc 行为入口和详细参考资料合并到 Pi 的 misc Skill 目录。
COPY --chown=ctf:ctf agents/misc/ /home/ctf/.pi/agent/skills/misc/
COPY --chown=ctf:ctf skills/misc/ /home/ctf/.pi/agent/skills/misc/

# 构建结束恢复非特权默认用户。
USER ctf
