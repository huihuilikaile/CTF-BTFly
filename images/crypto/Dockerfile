# Crypto 镜像继承公共 Pi 环境和跨方向资料库。
FROM ctf-agent-pi-base:0.1.0

# 安装口令破解、任意精度数学、符号计算、密码算法和约束求解工具。
USER root
RUN apt-get -o Acquire::Retries=5 update \
    && apt-get -o Acquire::Retries=5 install -y --no-install-recommends \
        john \
        libgmp-dev \
    && rm -rf /var/lib/apt/lists/* \
    && python -m pip install --no-cache-dir \
        gmpy2==2.2.1 \
        pycryptodome==3.23.0 \
        sympy==1.14.0 \
        z3-solver==4.15.1.0

# 将 Crypto 行为入口和详细参考资料合并到 Pi 的 crypto Skill 目录。
COPY --chown=ctf:ctf agents/crypto/ /home/ctf/.pi/agent/skills/crypto/
COPY --chown=ctf:ctf skills/crypto/ /home/ctf/.pi/agent/skills/crypto/

# 构建结束恢复非特权默认用户。
USER ctf
