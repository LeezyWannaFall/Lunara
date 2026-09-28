FROM golang:1.26.0-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot

FROM alpine:3.23.0
RUN apk add --no-cache ca-certificates
COPY --from=build /out/bot /usr/local/bin/bot
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/bot"]
