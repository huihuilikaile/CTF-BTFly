FROM ctf-agent-pi-base:0.1.0

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

COPY --chown=ctf:ctf agents/crypto/ /home/ctf/.pi/agent/skills/crypto/
COPY --chown=ctf:ctf skills/crypto/ /home/ctf/.pi/agent/skills/crypto/
USER ctf
