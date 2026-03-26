FROM debian:stable-slim

RUN <<EOF
set -eux
apt-get update
apt-get install -y --no-install-recommends openssh-server
rm -rf /var/lib/apt/lists/*
mkdir -p /run/sshd
ssh-keygen -A
useradd -m alice
useradd -m bob
# bind-mounted .ssh dirs are owned by host UID, not container users
echo 'StrictModes no' >> /etc/ssh/sshd_config
EOF

EXPOSE 22

CMD ["/usr/sbin/sshd", "-D", "-e"]
