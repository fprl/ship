FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    curl \
    git \
    nodejs \
    npm \
    openssh-server \
    python3 \
    rsync \
    sudo \
  && rm -rf /var/lib/apt/lists/*

RUN useradd -m -s /bin/bash admin \
  && printf 'admin:admin\n' | chpasswd \
  && mkdir -p /home/admin/.ssh /run/sshd /etc/simple-vps /etc/caddy/simple-vps /etc/caddy/conf.d /var/apps /tmp/simple-deploy \
  && chown admin:admin /home/admin/.ssh \
  && chmod 700 /home/admin/.ssh

COPY packages/simple-vps/roles/infra/files/simple-vps /usr/local/bin/simple-vps
COPY packages/simple-deploy/tests/fake-vps/fake-systemctl /usr/local/bin/systemctl
COPY packages/simple-deploy/tests/fake-vps/fake-caddy /usr/local/bin/caddy

RUN chmod 0755 /usr/local/bin/simple-vps /usr/local/bin/systemctl /usr/local/bin/caddy \
  && printf '%s\n' '{"routes":[],"version":2}' > /etc/simple-vps/state.json \
  && printf '%s\n' 'admin ALL=(root) NOPASSWD: /usr/local/bin/simple-vps' > /etc/sudoers.d/simple-deploy \
  && chmod 0440 /etc/sudoers.d/simple-deploy

EXPOSE 22

CMD ["/usr/sbin/sshd", "-D", "-e"]
