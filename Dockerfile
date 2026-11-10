# Dockerfile for Cluster Control UI
FROM node:24-alpine AS builder

WORKDIR /app

COPY package*.json ./
RUN npm ci

COPY . .
RUN npm run build

# Pinned by digest (multi-arch index) instead of the floating nginx:alpine.
# 1.31.8 carries the fixes for the nginx CVEs of 2026-10-14 (CVE-2026-15176,
# -93479, -95101, -97205, -97629; fixed from 1.31.7 / 1.30.6). The digest is the
# 2026-11-06 rebuild on Alpine 3.24.4, which carries brotli-libs 1.2.1-r0
# (CVE-2026-111497). Renovate keeps tag and digest current.
FROM nginx:1.31.8-alpine@sha256:713fc2ebfd92e8217db451477182ee91944b123d2b1ee9fd5a8c6e91ca6c3a11

# La base épinglée embarque les paquets Alpine du jour de sa publication : on
# applique les correctifs publiés depuis (ex. libxslt 1.1.43-r4, CVE-2026-87636
# et CVE-2026-9947), comme apt-get upgrade pour provisiond.
RUN apk upgrade --no-cache

COPY --from=builder /app/dist /usr/share/nginx/html

COPY deploy/docker/nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 8080

CMD ["nginx", "-g", "daemon off;"]
