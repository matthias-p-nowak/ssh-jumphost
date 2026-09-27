# SSH jump host: minimal image containing only sshd and the jumper binary
# (see docs/design.md "Image"; pattern from the sftp-server project).

# ----- 1. build the jumper binary (static) -----
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jumper ./cmd/jumper

# ----- 2. collect sshd, its libraries and the jumper files into /stage -----
FROM alpine:3.22 AS stage
# openssh-client-common: contains /etc/ssh/moduli (DH group exchange) on Alpine
RUN apk add --no-cache openssh-server openssh-client-common
COPY rootfs/ /stage/
COPY --from=build /out/jumper /stage/usr/local/bin/jumper

# sshd programs (sshd-auth exists from OpenSSH 10) plus every library ldd lists for them.
RUN set -e; \
    for f in /usr/sbin/sshd /usr/lib/ssh/sshd-session /usr/lib/ssh/sshd-auth; do \
        [ -e "$f" ] || continue; \
        echo "$f"; \
        ldd "$f" | awk '$2 == "=>" { print $3 } $1 ~ /^\// { print $1 }'; \
    done | sort -u > /tmp/files; \
    while read -r p; do \
        mkdir -p "/stage$(dirname "$p")"; \
        cp -L "$p" "/stage$p"; \
    done < /tmp/files; \
    cp -a /lib/libc.musl-* /stage/lib/; \
    cp /etc/ssh/moduli /stage/usr/share/jumper/etc/ssh/moduli; \
    chmod 600 /stage/usr/share/jumper/etc/shadow; \
    chmod 755 /stage/usr/local/bin/jumper; \
    mkdir -p /stage/etc /stage/run /stage/var/empty /stage/tmp; \
    chmod 1777 /stage/tmp; \
    cat /tmp/files

# ----- 3. final image -----
FROM scratch
COPY --from=stage /stage /

VOLUME [ "/etc" ]
EXPOSE 22/tcp

ENTRYPOINT [ "/usr/local/bin/jumper", "init" ]
