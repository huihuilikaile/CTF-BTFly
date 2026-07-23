FROM ctf-agent-pi-base:0.1.0

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

COPY --chown=ctf:ctf agents/pwn/ /home/ctf/.pi/agent/skills/pwn/
COPY --chown=ctf:ctf skills/pwn/ /home/ctf/.pi/agent/skills/pwn/
USER ctf
