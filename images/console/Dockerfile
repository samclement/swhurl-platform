# The web console (docs/console.md). Build from the repository root:
#   podman build -f images/console/Dockerfile -t swhurl-console .
# The console itself is v2's Go server (swhurl-v2 console); it reaches the API server directly, so the image carries
# no kubectl. helm matches CI (.github/workflows/validate.yml) and sops is the tooling's. x86-64 only. Every download is checked against a pinned SHA-256. The runtime is
# Alpine (musl); the Go CLIs are static, and the deps stage matches it so wheels are musllinux.

FROM ghcr.io/astral-sh/uv:0.10.9@sha256:10902f58a1606787602f303954cea099626a4adb02acbac4c69920fe9d278f82 AS uv

# v2 is one static binary and the console is part of it: the server, the pages, the jobs and the changes are Go,
# and Copier (still Python) renders a new app's repository for it. The pinned toolchain is images/backup's.
FROM docker.io/library/golang:1.26.0-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c AS v2
WORKDIR /src
COPY v2/go.mod v2/go.sum ./
RUN GOTOOLCHAIN=go1.26.0 GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org go mod download go.yaml.in/yaml/v3
COPY v2/ ./
RUN GOTOOLCHAIN=go1.26.0 GOPROXY=off GOSUMDB=off CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/swhurl-v2 ./cmd/swhurl-v2

FROM docker.io/library/python:3.12-slim-bookworm@sha256:1aaa65a85fda306ffb8b910824d4e93bdce61e212c7e87168123ea3073b41a1a AS tools
ARG HELM_VERSION=v3.19.0
ARG HELM_SHA256=a7f81ce08007091b86d8bd696eb4d86b8d0f2e1b9f6c714be62f82f96a594496
ARG SOPS_VERSION=v3.12.1
ARG SOPS_SHA256=cf3136d20a6004405c986a48e179c3c7a731f777fbf105a37bd5dac5c9047100
RUN set -eux; \
    apt-get update; apt-get install -y --no-install-recommends curl ca-certificates; \
    mkdir /out; cd /tmp; \
    curl -fsSLo helm.tgz "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz"; \
    echo "${HELM_SHA256}  helm.tgz" | sha256sum -c -; tar -xzf helm.tgz linux-amd64/helm; install -m 0755 linux-amd64/helm /out/helm; \
    curl -fsSLo sops "https://github.com/getsops/sops/releases/download/${SOPS_VERSION}/sops-${SOPS_VERSION}.linux.amd64"; \
    echo "${SOPS_SHA256}  sops" | sha256sum -c -; install -m 0755 sops /out/sops

FROM docker.io/library/python:3.12-alpine@sha256:4c47124a8391cb7a9f571164147d154777cf012a4ece5f86097130d7a4478111 AS deps
COPY --from=uv /uv /usr/local/bin/uv
WORKDIR /app
COPY pyproject.toml uv.lock ./
# Only Copier and its transitive dependencies, exactly as locked.
RUN UV_PROJECT_ENVIRONMENT=/app/.venv uv sync --frozen --only-group copier --no-cache --no-install-project

FROM docker.io/library/python:3.12-alpine@sha256:4c47124a8391cb7a9f571164147d154777cf012a4ece5f86097130d7a4478111
# No curl: PRs and new repositories go through GitHub's API, in the v2 binary. git only for Copier, the venv's, which
# clones a stack's template to render a new app repository. The base image's ca-certificates serve git and helm.
RUN apk add --no-cache git
COPY --from=tools /out/ /usr/local/bin/
COPY --from=v2 /out/swhurl-v2 /usr/local/bin/swhurl-v2
COPY --from=deps /app/.venv /app/.venv
ENV PATH=/app/.venv/bin:$PATH \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HOME=/tmp \
    XDG_CACHE_HOME=/tmp/cache
WORKDIR /app
USER 65532:65532
EXPOSE 8080
CMD ["swhurl-v2", "console", "--port", "8080", "--in-cluster"]
# Last, so a new revision does not invalidate the cached layers above.
ARG SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/samclement/swhurl-platform" \
      org.opencontainers.image.description="swhurl platform web console" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"
ENV SWHURL_SOURCE_REVISION=${SOURCE_REVISION}
