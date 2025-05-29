FROM docker.io/library/golang:1.24 AS builder

WORKDIR /walletd

# Install dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source
COPY . .

# Enable CGO for sqlite3 support
ENV CGO_ENABLED=1

RUN go generate ./...
RUN go build -o bin/ -tags='netgo timetzdata' -trimpath -a -ldflags '-s -w -linkmode external -extldflags "-static"'  ./cmd/walletd

FROM debian:bookworm-slim
LABEL maintainer="The Sia Foundation <info@sia.tech>" \
    org.opencontainers.image.description.vendor="The Sia Foundation" \
    org.opencontainers.image.description="A walletd container - send and receive Siacoins and Siafunds" \
    org.opencontainers.image.source="https://github.com/SiaFoundation/walletd" \
    org.opencontainers.image.licenses=MIT


# copy binary and prepare data dir.
COPY --from=builder /walletd/bin/* /usr/bin/
VOLUME [ "/data" ]

# API port
EXPOSE 9980/tcp
# RPC port
EXPOSE 9981/tcp

ENV WALLETD_DATA_DIR=/data
ENV WALLETD_CONFIG_FILE=/data/walletd.yml

ENTRYPOINT [ "walletd", "--http", ":9980" ]
