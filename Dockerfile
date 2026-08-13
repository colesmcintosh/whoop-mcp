FROM oven/bun:1-alpine
WORKDIR /app

COPY package.json bun.lock ./
RUN bun install --frozen-lockfile --production

COPY src ./src

RUN mkdir -p /data && chown bun:bun /data

# HTTP is the only useful transport in a container. Whoop rotates refresh
# tokens, so the file must live on a volume (see docker-compose.yml).
ENV PORT=8080
ENV WHOOP_TOKEN_FILE=/data/token.json
EXPOSE 8080
VOLUME /data
USER bun

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
  CMD bun -e "fetch('http://127.0.0.1:8080/healthz').then(r=>process.exit(r.ok?0:1)).catch(()=>process.exit(1))"

ENTRYPOINT ["bun", "src/cli/whoop-mcp.ts"]
