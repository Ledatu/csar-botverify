# syntax=docker/dockerfile:1
FROM golang:1.25-alpine AS builder

ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.Version=${VERSION}" -o /csar-botverify ./cmd/csar-botverify

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
RUN adduser -D -u 10001 botverify
COPY --from=builder /csar-botverify /usr/local/bin/csar-botverify
USER botverify
ENTRYPOINT ["csar-botverify"]
