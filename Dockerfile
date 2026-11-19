# syntax=docker/dockerfile:1
FROM golang:1.26.11-trixie@sha256:fa340cb59ebeb1d53ac1cccfc334201d938fe5f3458003cccb767096d8e032b8 AS dependencies
WORKDIR /src/panda-connect
COPY go.mod go.sum ./
COPY server/go.mod server/go.sum ./server/
RUN go mod download && cd server && go mod download
# The Go module zip excludes nested modules needed by the frontend. Fetch the
# full source at the server module's pinned commit, with a checksum.
ADD --checksum=sha256:5b6a555c0846207f9af753607c5bf6a08e310abdc8e464b0a8c68b88697fa0ff https://codeload.github.com/prusa3d/prusa-connect/tar.gz/ca11b9522aad2a0e15a6c0441c9ab0782d9e890a /tmp/prusa-connect.tar.gz
RUN mkdir /src/prusa-connect && tar -xzf /tmp/prusa-connect.tar.gz --strip-components=1 -C /src/prusa-connect

# Only static assets leave this stage, so run Node natively rather than under QEMU.
FROM --platform=$BUILDPLATFORM node:24-alpine@sha256:06d7adf3e0ef61a13f1d28682726366f97a15aa1888c934d3ecec86c33918b53 AS frontend
ENV PNPM_HOME=/pnpm
ENV PATH="$PNPM_HOME:$PATH" HUSKY=0 CI=true
RUN corepack enable
WORKDIR /src/prusa-connect
COPY --from=dependencies /src/prusa-connect/packages/standards ./packages/standards
COPY --from=dependencies /src/prusa-connect/packages/typescript ./packages/typescript
RUN cd packages/typescript && pnpm install --frozen-lockfile && pnpm run build
COPY --from=dependencies /src/prusa-connect/docs/errors/error-codes.json ./docs/errors/error-codes.json
COPY --from=dependencies /src/prusa-connect/packages/svelte ./packages/svelte
RUN cd packages/svelte && pnpm install --frozen-lockfile --ignore-scripts && pnpm run build
COPY --from=dependencies /src/prusa-connect/packages/fractal/client ./packages/fractal/client
RUN cd packages/fractal/client && pnpm install --frozen-lockfile && pnpm run build
COPY --from=dependencies /src/prusa-connect/frontend ./frontend
RUN cd frontend && pnpm install --frozen-lockfile && pnpm run build

FROM dependencies AS builder
COPY . .
# Go embeds assets from its downloaded dependency. Only this image's private
# module cache is changed; neither dependency is replaced with a local checkout.
COPY --from=frontend /src/prusa-connect/frontend/dist /go/pkg/mod/github.com/prusa3d/prusa-connect@v0.1.0-beta.1.0.20261118080352-ca11b9522aad/internal/transport/http/httpapi/appdist
ENV CGO_ENABLED=0
RUN --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/panda-connect-init ./cmd/panda-connect-init && \
    go build -trimpath -o /out/panda-connect-probe ./cmd/panda-connect-probe && \
    cd server && go build -trimpath -tags full -o /out/prusa-connect .

FROM debian:trixie-slim@sha256:3a39a0592364683e6bab97937b72cad5a8fa6dcbbee90edb3bb48c7f8e94f258
RUN apt-get update && apt-get install -y --no-install-recommends ffmpeg && rm -rf /var/lib/apt/lists/*
RUN groupadd --gid 10001 panda-connect && \
    useradd --uid 10001 --gid 10001 --no-create-home --shell /usr/sbin/nologin panda-connect && \
    install -d -o 10001 -g 10001 -m 0750 /data /etc/panda-connect
COPY --from=builder /out/ /usr/local/bin/
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY docker/config.yaml /etc/panda-connect/config.yaml
COPY docker/entrypoint.sh /usr/local/bin/panda-connect-entrypoint
USER 10001:10001
WORKDIR /data
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=5s --start-period=30s --retries=3 \
  CMD ["/usr/local/bin/prusa-connect", "core", "--config", "/data/config.yaml", "healthcheck"]
ENTRYPOINT ["/bin/sh", "/usr/local/bin/panda-connect-entrypoint"]
CMD ["core", "--config", "/data/config.yaml"]
