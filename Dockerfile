# syntax=docker/dockerfile:1

# Airlock 单镜像：靠子命令区分 control / edge / migrate 三个角色。
#
# 前两个阶段固定用 $BUILDPLATFORM——也就是构建机的原生架构——只有最终
# 镜像是 $TARGETPLATFORM。Go 的 CGO_ENABLED=0 交叉编译零成本，而让 QEMU
# 模拟目标架构去跑 Next.js 构建会把时间从分钟级拖到十几分钟，且 QEMU
# 偶发段错误会把人引向完全错误的排查方向。

# ---- 阶段一：构建控制台静态站 ----
FROM --platform=$BUILDPLATFORM node:22-alpine AS console
WORKDIR /src/web
# 先只拷依赖清单，让 npm ci 这一层能被缓存
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- 阶段二：编译 Go 二进制 ----
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# web/dist 在 .dockerignore 里被排掉了，这里从 console 阶段取
COPY --from=console /src/web/dist ./web/dist

ARG TARGETARCH
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/airlock ./cmd/airlock

# ---- 阶段三：运行时 ----
FROM alpine:3.21
# ca-certificates 不能省：OIDC 要连客户的 IdP，多半是 HTTPS，
# 缺了它就是一个极难查的 x509: certificate signed by unknown authority。
# wget 用于 compose 的 healthcheck（alpine 自带 busybox wget）。
RUN apk add --no-cache ca-certificates tzdata \
    && adduser -D -u 10001 airlock
COPY --from=build /out/airlock /usr/local/bin/airlock
USER airlock
ENTRYPOINT ["/usr/local/bin/airlock"]
