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

RUN npm install -g bun@1.3.14

RUN useradd -m -s /bin/bash -g operator operator \
  && useradd -m -s /bin/bash -U deploy \
  && printf 'operator:operator\n' | chpasswd \
  && printf 'deploy:deploy\n' | chpasswd \
  && mkdir -p /home/deploy/.ssh /run/sshd /etc/simple-vps /etc/caddy/simple-vps /etc/caddy/conf.d /var/apps /tmp/simple-deploy \
  && chown deploy:deploy /home/deploy/.ssh \
  && chmod 700 /home/deploy/.ssh

COPY packages/simple-vps/roles/infra/files/simple-vps /usr/local/bin/simple-vps
COPY packages/cli/tests/fake-vps/fake-systemctl /usr/local/bin/systemctl
COPY packages/cli/tests/fake-vps/fake-journalctl /usr/local/bin/journalctl
COPY packages/cli/tests/fake-vps/fake-caddy /usr/local/bin/caddy

RUN chmod 0755 /usr/local/bin/simple-vps /usr/local/bin/systemctl /usr/local/bin/journalctl /usr/local/bin/caddy \
  && printf '%s\n' '{"routes":[],"version":2}' > /etc/simple-vps/state.json \
  && printf '%s\n' 'operator ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/operator \
  && printf '%s\n' 'deploy ALL=(root) NOPASSWD: /usr/local/bin/simple-vps' > /etc/sudoers.d/simple-vps \
  && chmod 0440 /etc/sudoers.d/operator /etc/sudoers.d/simple-vps

EXPOSE 22

CMD ["/usr/sbin/sshd", "-D", "-e"]
