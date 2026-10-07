# The incident reviewer's analysis worker (docs/plan.md section 14). Build from the repository root:
#   podman build -f images/incident-review-worker/Dockerfile -t swhurl-incident-review-worker .
# It holds the pinned Codex CLI, the wrapper, the prompt and the diagnosis schema, and nothing
# else: no kubectl, no swhurl tooling, no git. The model key is mounted at run time, never built in.
# x86-64 only. The download is checked against a pinned SHA-256; the CLI is a static musl binary.

FROM docker.io/library/python:3.12-slim-bookworm@sha256:1aaa65a85fda306ffb8b910824d4e93bdce61e212c7e87168123ea3073b41a1a AS tools
ARG CODEX_VERSION=0.160.1
ARG CODEX_SHA256=9226581be592d18f7e7f740a352fdb63aa61e45e39f7eb9b09d3888c84bba33f
RUN set -eux; \
    apt-get update; apt-get install -y --no-install-recommends curl ca-certificates; \
    mkdir /out; cd /tmp; \
    curl -fsSLo codex.tgz "https://github.com/openai/codex/releases/download/rust-v${CODEX_VERSION}/codex-x86_64-unknown-linux-musl.tar.gz"; \
    echo "${CODEX_SHA256}  codex.tgz" | sha256sum -c -; tar -xzf codex.tgz codex-x86_64-unknown-linux-musl; \
    install -m 0755 codex-x86_64-unknown-linux-musl /out/codex

FROM docker.io/library/alpine:3.22@sha256:3e9b4b680bfc9fb5269227cffbd6d42be39fbf7c0b908123913864aa4447e764
# bash for the wrapper; bubblewrap because the CLI refuses to run a model-requested command
# without it; the base image's CA bundle serves the model endpoint.
RUN apk add --no-cache bash bubblewrap
COPY --from=tools /out/codex /usr/local/bin/codex
COPY images/incident-review-worker/analyse.sh images/incident-review-worker/prompt.md /opt/review/
COPY tools/swhurl/incident_review/diagnosis.schema.json /opt/review/
ENV HOME=/scratch
USER 65532:65532
ENTRYPOINT ["/opt/review/analyse.sh"]
