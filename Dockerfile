FROM node:24-alpine AS frontend
WORKDIR /build/web
COPY web/package*.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.27.3-alpine AS backend
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /kubepilot ./cmd/kubepilot

FROM alpine:3.22
RUN apk add --no-cache ca-certificates && addgroup -g 10001 agent && adduser -D -u 10001 -G agent agent && mkdir -p /app/data && chown -R agent:agent /app
WORKDIR /app
COPY --from=backend /kubepilot /app/kubepilot
COPY --from=frontend /build/web/dist /app/web/dist
USER 10001:10001
ENV AGENT_ADDR=0.0.0.0:18080 AGENT_MODE=demo AGENT_DATA_DIR=/app/data
EXPOSE 18080
ENTRYPOINT ["/app/kubepilot"]
