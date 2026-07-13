FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update \
  && apt-get install -y --no-install-recommends \
    bash \
    ca-certificates \
    curl \
    git \
    openssl \
    openssh-server \
    python3 \
    rsync \
    sqlite3 \
    sudo \
  && rm -rf /var/lib/apt/lists/*

RUN useradd -m -s /bin/bash -g operator operator \
  && useradd -m -s /bin/bash -U deploy \
  && printf 'operator:operator\n' | chpasswd \
  && printf 'deploy:deploy\n' | chpasswd \
  && mkdir -p /home/deploy/.ssh /run/sshd /etc/ship /etc/caddy/conf.d /var/apps \
  && chown deploy:deploy /home/deploy/.ssh \
  && chmod 700 /home/deploy/.ssh

COPY .fake-vps-bin/ship-linux-amd64 /usr/local/bin/ship
COPY --from=docker.io/library/caddy:2-alpine /usr/bin/caddy /usr/local/bin/caddy-real
COPY tests/fake-vps/fake-systemctl /usr/local/bin/systemctl
COPY tests/fake-vps/fake-caddy /usr/local/bin/caddy
COPY tests/fake-vps/fake-podman /usr/local/bin/podman
COPY tests/fake-vps/fake-podman.app.py /usr/local/bin/fake-podman.app.py
COPY tests/fake-vps/fake-podman.caddy.py /usr/local/bin/fake-podman.caddy.py

RUN chmod 0755 /usr/local/bin/ship /usr/local/bin/systemctl /usr/local/bin/caddy /usr/local/bin/podman \
  && chmod 0644 /usr/local/bin/fake-podman.app.py /usr/local/bin/fake-podman.caddy.py \
  && printf '%s\n' 'operator ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/operator \
  && printf '%s\n' 'deploy ALL=(root) NOPASSWD: /usr/local/bin/ship server app *, /usr/local/bin/ship server doctor, /usr/local/bin/ship server doctor *, /usr/local/bin/ship server key *, /usr/local/bin/ship server approval *, /usr/local/bin/ship server notify *, /usr/local/bin/ship server version, /usr/local/bin/ship server version *, /usr/local/bin/ship server update *' > /etc/sudoers.d/ship \
  && chmod 0440 /etc/sudoers.d/operator /etc/sudoers.d/ship

EXPOSE 22

CMD ["/usr/sbin/sshd", "-D", "-e"]
