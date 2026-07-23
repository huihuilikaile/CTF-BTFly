FROM ctf-agent-pi-base:0.1.0

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

COPY --chown=ctf:ctf agents/forensics/ /home/ctf/.pi/agent/skills/forensics/
COPY --chown=ctf:ctf skills/forensics/ /home/ctf/.pi/agent/skills/forensics/
USER ctf
