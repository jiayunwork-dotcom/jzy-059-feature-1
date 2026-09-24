# ---- 构建阶段：拉依赖、跑测试（随构建一并运行）、编译 ----
FROM golang:1.22-alpine AS build

# gcc/musl-dev 供 `go test -race` 使用；ca-certificates 供 HTTPS 拉模块
RUN apk add --no-cache gcc musl-dev git ca-certificates

WORKDIR /src

# 先拷模块定义，尽量利用构建缓存
COPY go.mod go.sum* ./
RUN go mod download

COPY . .

# 稳性求解、力臂扫描、单位换算、非法参数拦截、并发隔离全部测试随构建运行；
# -race 同时盯出数据竞争。
RUN go vet ./... && CGO_ENABLED=1 go test -race -count=1 ./...

RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 运行阶段：最小镜像，非 root，容器启动即提供服务 ----
FROM alpine:3.20

RUN apk add --no-cache ca-certificates wget \
    && addgroup -S app && adduser -S app -G app \
    && mkdir -p /data && chown app:app /data

COPY --from=build /out/server /usr/local/bin/server

USER app
ENV GIN_MODE=release \
    PORT=8080 \
    DATA_FILE=/data/conditions.json

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:8080/health || exit 1

ENTRYPOINT ["server"]
