FROM ctf-agent-pi-base:0.1.0

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

COPY --chown=ctf:ctf agents/reverse/ /home/ctf/.pi/agent/skills/reverse/
COPY --chown=ctf:ctf skills/reverse/ /home/ctf/.pi/agent/skills/reverse/
USER ctf
