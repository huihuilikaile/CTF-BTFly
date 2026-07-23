FROM ctf-agent-pi-base:0.1.0

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

COPY --chown=ctf:ctf agents/web/ /home/ctf/.pi/agent/skills/web/
COPY --chown=ctf:ctf skills/web/ /home/ctf/.pi/agent/skills/web/
USER ctf
