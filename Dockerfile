FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /hhc-web-api ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /hhc-web-migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /hhc-web-api /hhc-web-api
COPY --from=build /hhc-web-migrate /hhc-web-migrate
EXPOSE 8082
USER nonroot:nonroot
ENTRYPOINT ["/hhc-web-api"]
