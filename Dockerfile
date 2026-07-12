FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /hhc-web-api ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /hhc-web-api /hhc-web-api
EXPOSE 8082
USER nonroot:nonroot
ENTRYPOINT ["/hhc-web-api"]
