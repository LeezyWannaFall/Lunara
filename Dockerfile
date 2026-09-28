FROM golang:1.26.0-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/bot ./cmd/bot
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/admin ./cmd/admin

FROM alpine:3.23.0
RUN apk add --no-cache ca-certificates
COPY --from=build /out/bot /usr/local/bin/bot
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY --from=build /out/admin /usr/local/bin/admin
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/bot"]
