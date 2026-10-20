# Caddy with one DNS provider compiled in.
#
# The stock image cannot answer an ACME DNS challenge, and a DNS challenge is
# the only kind that works here: the HTTPS names resolve to a private address
# (a tailnet, a LAN) that Let's Encrypt can never connect to, so HTTP-01 and
# TLS-ALPN-01 fail by construction. xcaddy builds the same Caddy with the
# Cloudflare module added.
FROM caddy:2.11.6-builder AS build
RUN xcaddy build --with github.com/caddy-dns/cloudflare@v0.2.4

FROM caddy:2.11.6
COPY --from=build /usr/bin/caddy /usr/bin/caddy
