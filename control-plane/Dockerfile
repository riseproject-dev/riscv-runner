# SPDX-License-Identifier: MIT
FROM golang:1.27-trixie AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ghfe ./cmd/ghfe \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/scheduler ./cmd/scheduler

FROM gcr.io/distroless/base-debian13 AS base
WORKDIR /app

FROM base AS ghfe
COPY --from=build /out/ghfe /app/ghfe
ENTRYPOINT ["/app/ghfe"]

FROM base AS scheduler
COPY --from=build /out/scheduler /app/scheduler
ENTRYPOINT ["/app/scheduler"]
