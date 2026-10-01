FROM golang:1.22-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .

# 登录页版本号经 ldflags -X main.Version 注入（见 main.go Version 注释）。
# release.yml 的 build-docker 会传 tag 版本；本地 docker build 不传时回退 dev。
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -ldflags="-X main.Version=${VERSION}" -o qoder2api .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=builder /build/qoder2api .
ENV QODER_HOST=0.0.0.0 \
    QODER_PORT=10081 \
    TZ=Asia/Shanghai
EXPOSE 10081
CMD ["./qoder2api"]
