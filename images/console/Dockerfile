# The web console (docs/plan.md section 7). Build from the repository root:
#   podman build -f images/console/Dockerfile -t swhurl-console .
# kubectl and helm versions match CI (.github/workflows/validate.yml). No flux CLI:
# the console patches Flux objects with kubectl (tools/swhurl/console/actions.py).
# x86-64 only. Every download is checked against a pinned SHA-256. The runtime is
# Alpine (musl); the Go CLIs are static, and the deps stage matches it so wheels are musllinux.

FROM ghcr.io/astral-sh/uv:0.10.9@sha256:10902f58a1606787602f303954cea099626a4adb02acbac4c69920fe9d278f82 AS uv

# v2 is one static binary: the console writes v2's records by running it, never by modelling them in Python
# (docs/v2-deletion-plan.md section 5, decision 2). The pinned toolchain is the one images/backup/Dockerfile uses.
FROM docker.io/library/golang:1.26.0-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c AS v2
WORKDIR /src
COPY v2/ ./
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/swhurl-v2 ./cmd/swhurl-v2

FROM docker.io/library/python:3.12-slim-bookworm@sha256:1aaa65a85fda306ffb8b910824d4e93bdce61e212c7e87168123ea3073b41a1a AS tools
ARG KUBECTL_VERSION=v1.34.4
ARG KUBECTL_SHA256=d50c359d95e0841eaad08ddc27c7be37cba8fdccfba5c8e2ded65e121ff112db
ARG HELM_VERSION=v3.19.0
ARG HELM_SHA256=a7f81ce08007091b86d8bd696eb4d86b8d0f2e1b9f6c714be62f82f96a594496
ARG SOPS_VERSION=v3.12.1
ARG SOPS_SHA256=cf3136d20a6004405c986a48e179c3c7a731f777fbf105a37bd5dac5c9047100
RUN set -eux; \
    apt-get update; apt-get install -y --no-install-recommends curl ca-certificates; \
    mkdir /out; cd /tmp; \
    curl -fsSLo kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/amd64/kubectl"; \
    echo "${KUBECTL_SHA256}  kubectl" | sha256sum -c -; install -m 0755 kubectl /out/kubectl; \
    curl -fsSLo helm.tgz "https://get.helm.sh/helm-${HELM_VERSION}-linux-amd64.tar.gz"; \
    echo "${HELM_SHA256}  helm.tgz" | sha256sum -c -; tar -xzf helm.tgz linux-amd64/helm; install -m 0755 linux-amd64/helm /out/helm; \
    curl -fsSLo sops "https://github.com/getsops/sops/releases/download/${SOPS_VERSION}/sops-${SOPS_VERSION}.linux.amd64"; \
    echo "${SOPS_SHA256}  sops" | sha256sum -c -; install -m 0755 sops /out/sops

FROM docker.io/library/python:3.12-alpine@sha256:4c47124a8391cb7a9f571164147d154777cf012a4ece5f86097130d7a4478111 AS deps
COPY --from=uv /uv /usr/local/bin/uv
WORKDIR /app
COPY pyproject.toml uv.lock ./
# The check and console groups (the defaults), exactly as locked; nothing else.
RUN UV_PROJECT_ENVIRONMENT=/app/.venv uv sync --frozen --no-cache --no-install-project

FROM docker.io/library/python:3.12-alpine@sha256:4c47124a8391cb7a9f571164147d154777cf012a4ece5f86097130d7a4478111
# No curl: PRs and new repositories go through GitHub's API from Python (tools/swhurl/console/github.py,
# proposal.py, repos.py). git only for Copier, which clones a stack's template to render a new app repository.
# The base image's ca-certificates serve kubectl, helm and git.
RUN apk add --no-cache git
COPY --from=tools /out/ /usr/local/bin/
COPY --from=v2 /out/swhurl-v2 /usr/local/bin/swhurl-v2
COPY --from=deps /app/.venv /app/.venv
COPY tools/ /app/tools/
# The tooling reads BASE_DOMAIN from here when it is imported.
COPY clusters/home/flux-system/sources/configmap-platform-settings.yaml /app/clusters/home/flux-system/sources/
ENV PATH=/app/.venv/bin:$PATH \
    PYTHONPATH=/app/tools \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    HOME=/tmp \
    XDG_CACHE_HOME=/tmp/cache
WORKDIR /app
USER 65532:65532
EXPOSE 8080
CMD ["python", "-m", "swhurl", "console", "--port", "8080"]
# Last, so a new revision does not invalidate the cached layers above.
ARG SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/samclement/swhurl-platform" \
      org.opencontainers.image.description="swhurl platform web console" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"
ENV SWHURL_SOURCE_REVISION=${SOURCE_REVISION}
