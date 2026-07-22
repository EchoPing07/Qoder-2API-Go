FROM golang:1.22-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o qoder2api .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /build/qoder2api .
COPY baseprompt.json .
ENV QODER_HOST=0.0.0.0 \
    QODER_PORT=18080
EXPOSE 18080
CMD ["./qoder2api"]
