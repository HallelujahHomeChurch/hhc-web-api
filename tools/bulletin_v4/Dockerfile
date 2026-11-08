ARG BASE_IMAGE
FROM golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -o /postgres.test ./internal/postgres \
 && test -s /postgres.test

# Inherit the exact production helper, model, Python and Poppler environment.
FROM ${BASE_IMAGE}
COPY --from=build --chmod=0555 /postgres.test /v4/postgres.test
COPY internal/onlinebulletins/testdata/1739-sanitized.pdf /v4/1739-sanitized.pdf
ENTRYPOINT ["/v4/postgres.test","-test.run=^TestBulletinV4DistributedLab$","-test.v","-test.timeout=5m"]
