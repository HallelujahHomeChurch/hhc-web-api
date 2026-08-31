FROM golang@sha256:1e0126852075c9c60731c8ba49088448b91f63e2aed97ca9d1a9791622a05946 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /hhc-web-api ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /hhc-web-migrate ./cmd/migrate \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /hhc-web-content-import ./cmd/content-import \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /hhc-web-home-v2-migrate ./cmd/home-v2-migrate

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /hhc-web-api /hhc-web-api
COPY --from=build /hhc-web-migrate /hhc-web-migrate
COPY --from=build /hhc-web-content-import /hhc-web-content-import
COPY --from=build /hhc-web-home-v2-migrate /hhc-web-home-v2-migrate
EXPOSE 8082
USER nonroot:nonroot
ENTRYPOINT ["/hhc-web-api"]
