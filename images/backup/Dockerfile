# The backup jobs (docs/plan.md section 25): one CronJob per database runs
#   swhurl-backup backup-job-sqlite | restore-job-sqlite | backup-job-mongodb
# Build from the repository root:
#   podman build -f images/backup/Dockerfile -t swhurl-backup .
# x86-64 only. Debian, not Alpine: mongodump is a glibc binary and links libgssapi-krb5. Everything else --
# the database copy, the encryption, the upload -- is one Go binary, so there is no Python, no AWS CLI and no
# age CLI here. No kubectl: a job reaches nothing but its database and S3.

FROM docker.io/library/golang:1.26.0-bookworm@sha256:2a0ba12e116687098780d3ce700f9ce3cb340783779646aafbabed748fa6677c AS build
WORKDIR /src
COPY images/backup/tool/ ./
# Stripped: the image runs the binary, it does not debug it.
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/swhurl-backup .

FROM docker.io/library/debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8 AS tools
ARG MONGO_TOOLS_VERSION=100.19.1
ARG MONGO_TOOLS_SHA256=9ba637b2fee9cd5afd83ea93b688a0f6bf3e0e4a614d3b0c55f63fa66ae62e2f
RUN set -eux; \
    apt-get update; apt-get install -y --no-install-recommends ca-certificates curl; \
    cd /tmp; \
    curl -fsSLo tools.tgz "https://fastdl.mongodb.org/tools/db/mongodb-database-tools-debian12-x86_64-${MONGO_TOOLS_VERSION}.tgz"; \
    echo "${MONGO_TOOLS_SHA256}  tools.tgz" | sha256sum -c -; tar -xzf tools.tgz; \
    mkdir -p /out; \
    install -m 0755 "mongodb-database-tools-debian12-x86_64-${MONGO_TOOLS_VERSION}/bin/mongodump" /out/mongodump

FROM docker.io/library/debian:bookworm-slim@sha256:7ff6ba31527b9e336162fa061746c366c4b2032f4d2cff2078bea43f78efe3f8
RUN set -eux; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates libgssapi-krb5-2; \
    rm -rf /var/lib/apt/lists/*
COPY --from=build /out/swhurl-backup /usr/local/bin/swhurl-backup
COPY --from=tools /out/mongodump /usr/local/bin/mongodump
# The jobs read the region and a scratch HOME; the pod's environment states everything else.
ENV AWS_DEFAULT_REGION=eu-west-2 \
    HOME=/tmp
WORKDIR /scratch
# A SQLite job runs as its app's user (the pod's securityContext states the uid too); this is MongoDB's default.
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/swhurl-backup"]
ARG SOURCE_REVISION=unknown
LABEL org.opencontainers.image.source="https://github.com/samclement/swhurl-platform" \
      org.opencontainers.image.description="swhurl platform backup jobs" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"
