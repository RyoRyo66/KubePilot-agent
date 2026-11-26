FROM node:24-bookworm-slim@sha256:873f586f3794f52281bd039bf02c048a0be08fde619338f9941bad1eb6b34530 AS frontend
WORKDIR /build
COPY web/package*.json ./
RUN npm ci --no-fund --no-audit
COPY web/ ./
RUN npm run build

FROM node:24-bookworm-slim@sha256:873f586f3794f52281bd039bf02c048a0be08fde619338f9941bad1eb6b34530 AS base
WORKDIR /app
ENV NODE_ENV=production
COPY package.json ./
RUN mkdir -p /app/data && chown node:node /app/data
USER node
FROM base AS connector
COPY connector/main.js connector/kube.js connector/policy.js connector/admission.js connector/
COPY server/common.js server/network.js server/evidence.js server/storage.js server/
CMD ["node","connector/main.js"]
FROM base AS platform
COPY server/ server/
COPY connector/ connector/
COPY ops/ ops/
COPY --from=frontend /build/dist web/dist
CMD ["node","server/main.js"]
