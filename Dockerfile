# Dockerfile for Cluster Control UI
FROM node:24-alpine AS builder

WORKDIR /app

COPY package*.json ./
RUN npm ci

COPY . .
RUN npm run build

# Pinned by digest (multi-arch index) instead of the floating nginx:alpine.
# 1.31.8 carries the fixes for the nginx CVEs of 2026-10-14 (CVE-2026-15176,
# -93479, -95101, -97205, -97629; fixed from 1.31.7 / 1.30.6). Renovate keeps
# tag and digest current.
FROM nginx:1.31.8-alpine@sha256:1ef7ca94d7879df82421428ce13bd1eaa412404953859cf7a5b6512c25e67a60

COPY --from=builder /app/dist /usr/share/nginx/html

COPY deploy/docker/nginx.conf /etc/nginx/conf.d/default.conf

EXPOSE 8080

CMD ["nginx", "-g", "daemon off;"]
