# SPDX-License-Identifier: MIT
FROM --platform=$BUILDPLATFORM golang:1.27-trixie AS build
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/k8s-device-plugin ./cmd/k8s-device-plugin

FROM gcr.io/distroless/base-debian13
WORKDIR /app
COPY --from=build /out/k8s-device-plugin /app/k8s-device-plugin
ENTRYPOINT ["/app/k8s-device-plugin"]
