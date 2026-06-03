FROM golang:alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=HEAD
RUN CGO_ENABLED=0 go build -ldflags="-s -w -X main.version=${VERSION}" -trimpath -o /ssh-gateway ./cmd/ssh-gateway

FROM alpine:latest

RUN apk add --no-cache openssh-server

COPY --from=builder /ssh-gateway /usr/local/bin/ssh-gateway

VOLUME /etc/ssh /home
EXPOSE 22

CMD ["ssh-gateway"]
