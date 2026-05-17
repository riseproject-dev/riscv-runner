FROM --platform=$BUILDPLATFORM golang:1.22-alpine AS builder

ARG TARGETARCH
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH go build -o k8s-device-plugin ./cmd/k8s-device-plugin

FROM alpine:3.20
COPY --from=builder /app/k8s-device-plugin /usr/local/bin/k8s-device-plugin
ENTRYPOINT ["/usr/local/bin/k8s-device-plugin"]
