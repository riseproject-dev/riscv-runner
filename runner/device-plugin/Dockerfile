# SPDX-License-Identifier: MIT
FROM --platform=$BUILDPLATFORM golang:1.26-trixie AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/k8s-device-plugin ./cmd/k8s-device-plugin \
 && CGO_ENABLED=0 GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/k8s-node-labeller ./cmd/k8s-node-labeller

FROM gcr.io/distroless/base-debian13 AS base
WORKDIR /app

FROM base AS device-plugin
COPY --from=build /out/k8s-device-plugin /app/k8s-device-plugin
ENTRYPOINT ["/app/k8s-device-plugin"]

FROM base AS node-labeller
COPY --from=build /out/k8s-node-labeller /app/k8s-node-labeller
ENTRYPOINT ["/app/k8s-node-labeller"]
