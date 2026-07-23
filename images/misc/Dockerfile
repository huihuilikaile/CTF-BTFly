FROM ctf-agent-pi-base:0.1.0

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

COPY --chown=ctf:ctf agents/misc/ /home/ctf/.pi/agent/skills/misc/
COPY --chown=ctf:ctf skills/misc/ /home/ctf/.pi/agent/skills/misc/
USER ctf
