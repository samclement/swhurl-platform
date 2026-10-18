# The backup jobs (docs/plan.md section 25): one CronJob per database runs
#   python -m swhurl backup-job-sqlite | backup-job-mongodb
# Build from the repository root:
#   podman build -f images/backup/Dockerfile -t swhurl-backup .
# x86-64 only. Every download is checked against a pinned SHA-256. Debian, not Alpine: the AWS CLI
# and the MongoDB database tools are built for glibc. No kubectl: a job reaches nothing but its
# database and S3.

FROM ghcr.io/astral-sh/uv:0.10.9@sha256:10902f58a1606787602f303954cea099626a4adb02acbac4c69920fe9d278f82 AS uv

FROM docker.io/library/python:3.12-slim-bookworm@sha256:1aaa65a85fda306ffb8b910824d4e93bdce61e212c7e87168123ea3073b41a1a AS tools
ARG AGE_VERSION=v1.3.2
ARG AGE_SHA256=cbe24006683f8eb669266162894b9a522a1af52f2665fbc63a4bb032ed26ac10
ARG MONGO_TOOLS_VERSION=100.19.1
ARG MONGO_TOOLS_SHA256=9ba637b2fee9cd5afd83ea93b688a0f6bf3e0e4a614d3b0c55f63fa66ae62e2f
ARG AWSCLI_VERSION=2.37.9
ARG AWSCLI_SHA256=6b3a6a3d7bb3997928f0bdf7b866914224abf242c2e54e1dcebbe84bee64f356
RUN set -eux; \
    apt-get update; apt-get install -y --no-install-recommends curl ca-certificates unzip; \
    mkdir -p /out/bin; cd /tmp; \
    curl -fsSLo age.tgz "https://github.com/FiloSottile/age/releases/download/${AGE_VERSION}/age-${AGE_VERSION}-linux-amd64.tar.gz"; \
    echo "${AGE_SHA256}  age.tgz" | sha256sum -c -; tar -xzf age.tgz age/age; install -m 0755 age/age /out/bin/age; \
    curl -fsSLo tools.tgz "https://fastdl.mongodb.org/tools/db/mongodb-database-tools-debian12-x86_64-${MONGO_TOOLS_VERSION}.tgz"; \
    echo "${MONGO_TOOLS_SHA256}  tools.tgz" | sha256sum -c -; tar -xzf tools.tgz; \
    install -m 0755 "mongodb-database-tools-debian12-x86_64-${MONGO_TOOLS_VERSION}/bin/mongodump" /out/bin/mongodump; \
    curl -fsSLo awscli.zip "https://awscli.amazonaws.com/awscli-exe-linux-x86_64-${AWSCLI_VERSION}.zip"; \
    echo "${AWSCLI_SHA256}  awscli.zip" | sha256sum -c -; unzip -q awscli.zip; \
    ./aws/install --install-dir /opt/aws-cli --bin-dir /tmp/aws-bin

FROM docker.io/library/python:3.12-slim-bookworm@sha256:1aaa65a85fda306ffb8b910824d4e93bdce61e212c7e87168123ea3073b41a1a AS deps
COPY --from=uv /uv /usr/local/bin/uv
WORKDIR /app
COPY pyproject.toml uv.lock ./
# The check and backup groups, exactly as locked; nothing else.
RUN UV_PROJECT_ENVIRONMENT=/app/.venv uv sync --frozen --no-cache --no-install-project --no-default-groups \
    --group check --group backup

FROM docker.io/library/python:3.12-slim-bookworm@sha256:1aaa65a85fda306ffb8b910824d4e93bdce61e212c7e87168123ea3073b41a1a
COPY --from=tools /out/bin/ /usr/local/bin/
# The CLI links its current version by absolute path: installed where it runs.
COPY --from=tools /opt/aws-cli /opt/aws-cli
RUN ln -s /opt/aws-cli/v2/current/bin/aws /usr/local/bin/aws
COPY --from=deps /app/.venv /app/.venv
COPY tools/ /app/tools/
# The tooling reads BASE_DOMAIN from here when it is imported.
COPY clusters/home/flux-system/sources/configmap-platform-settings.yaml /app/clusters/home/flux-system/sources/
ENV PATH=/app/.venv/bin:$PATH \
    PYTHONPATH=/app/tools \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HOME=/tmp \
    AWS_DEFAULT_REGION=eu-west-2
WORKDIR /app
# A SQLite job runs as its app's user (the pod's securityContext); this is the default for MongoDB.
USER 65532:65532
ENTRYPOINT ["python", "-m", "swhurl"]
ARG SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/samclement/swhurl-platform" \
      org.opencontainers.image.description="swhurl platform backup jobs" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"
