FROM golang:alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath -o /ssh-gateway ./cmd/ssh-gateway

FROM alpine:latest

RUN apk add --no-cache openssh-server

RUN <<EOF cat > /etc/ssh/sshd_config
Port 22
PermitRootLogin no
PasswordAuthentication no
PubkeyAuthentication yes
AuthorizedKeysFile .ssh/authorized_keys
AllowTcpForwarding yes
GatewayPorts no
X11Forwarding no
AllowAgentForwarding yes
ForceCommand /bin/false
PrintMotd no
EOF

COPY --from=builder /ssh-gateway /usr/local/bin/ssh-gateway

VOLUME /etc/ssh /home
EXPOSE 22

CMD ["ssh-gateway"]
