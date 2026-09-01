FROM node:25-alpine AS web
RUN corepack enable
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
COPY internal/webui/ /src/internal/webui/
RUN pnpm build

FROM golang:1.26.7-alpine AS build
ARG VERSION=dev
ARG COMMIT=unknown
ARG RELEASE_BASE=https://github.com/chnzzh/hostpin/releases/latest/download
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/internal/webui/dist /src/internal/webui/dist
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X github.com/chnzzh/hostpin/internal/buildinfo.Version=${VERSION} -X github.com/chnzzh/hostpin/internal/buildinfo.Commit=${COMMIT} -X github.com/chnzzh/hostpin/internal/buildinfo.ReleaseBase=${RELEASE_BASE}" -o /out/hostpin-server ./cmd/hostpin-server

FROM alpine:3.23
RUN apk add --no-cache ca-certificates tzdata && addgroup -S -g 10001 hostpin && adduser -S -D -H -u 10001 -G hostpin hostpin && mkdir -p /var/lib/hostpin && chown hostpin:hostpin /var/lib/hostpin
COPY --from=build /out/hostpin-server /usr/local/bin/hostpin-server
USER hostpin
VOLUME ["/var/lib/hostpin"]
EXPOSE 8080
ENV HOSTPIN_LISTEN=:8080 HOSTPIN_PUBLIC_URL=http://localhost:8080 HOSTPIN_DATA_DIR=/var/lib/hostpin
ENTRYPOINT ["/usr/local/bin/hostpin-server"]
CMD ["serve", "--config", "/var/lib/hostpin/hostpin.yaml"]
