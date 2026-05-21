FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/whoop-mcp ./cmd/whoop-mcp

FROM gcr.io/distroless/static-debian12:latest
COPY --from=build /out/whoop-mcp /whoop-mcp
ENV WHOOP_TOKEN_FILE=/data/token.json
ENTRYPOINT ["/whoop-mcp"]
