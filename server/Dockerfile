# syntax=docker/dockerfile:1.7
FROM node:22-alpine AS frontend
WORKDIR /src
COPY package.json package-lock.json ./
RUN npm ci
COPY index.html tsconfig*.json vite.config.ts eslint.config.js ./
COPY src ./src
COPY scripts ./scripts
COPY testdata ./testdata
RUN npm run build

FROM golang:1.26-alpine AS backend
WORKDIR /src/server
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/api ./cmd/api \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/migrate ./cmd/migrate \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/adminctl ./cmd/adminctl

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S -g 10001 app \
    && adduser -S -D -H -u 10001 -G app app
WORKDIR /app
COPY --from=backend --chown=app:app /out/api /app/api
COPY --from=backend --chown=app:app /out/migrate /app/migrate
COPY --from=backend --chown=app:app /out/adminctl /app/adminctl
COPY --from=backend --chown=app:app /src/server/migrations /app/migrations
COPY --from=frontend --chown=app:app /src/dist /app/public
USER app
ENV HTTP_ADDRESS=:9003 MIGRATIONS_DIR=/app/migrations STATIC_DIRECTORY=/app/public
EXPOSE 9003
ENTRYPOINT []
CMD ["/app/api"]
