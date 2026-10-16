FROM node:24-bookworm-slim AS node-build
WORKDIR /build
COPY runtime/package*.json runtime/
COPY web/package*.json web/
RUN npm --prefix runtime ci && npm --prefix web ci
COPY runtime/ runtime/
COPY web/ web/
RUN npm --prefix runtime run build && npm --prefix web run build && npm --prefix runtime prune --omit=dev

FROM golang:1.26-bookworm AS go-build
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/embed.go web/embed.go
COPY --from=node-build /build/web/dist web/dist
RUN CGO_ENABLED=0 go build -trimpath -o /siagent ./cmd/siagent

FROM node:24-bookworm-slim
ENV NODE_ENV=production SIAGENT_DATA_DIR=/data SIAGENT_LISTEN=0.0.0.0:8080
WORKDIR /app
RUN apt-get update && apt-get install -y --no-install-recommends poppler-utils && rm -rf /var/lib/apt/lists/*
COPY --from=go-build /siagent /app/siagent
COPY --from=node-build /build/runtime/dist /app/runtime/dist
COPY --from=node-build /build/runtime/node_modules /app/runtime/node_modules
COPY runtime/package.json /app/runtime/package.json
RUN install -d -m 0700 -o node -g node /data
USER node
VOLUME /data
EXPOSE 8080
STOPSIGNAL SIGTERM
HEALTHCHECK --interval=15s --timeout=3s --start-period=30s CMD ["node", "-e", "fetch('http://127.0.0.1:8080/health/ready').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"]
ENTRYPOINT ["/app/siagent"]
