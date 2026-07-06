FROM oven/bun:1-alpine
WORKDIR /app

COPY package.json bun.lock ./
RUN bun install --frozen-lockfile --production

COPY src ./src

ENV WHOOP_TOKEN_FILE=/data/token.json
ENTRYPOINT ["bun", "src/cli/whoop-mcp.ts"]
