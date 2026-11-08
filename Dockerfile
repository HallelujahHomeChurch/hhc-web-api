FROM --platform=$BUILDPLATFORM golang:1.26.10-bookworm@sha256:632dff7351799a5a9954a48dc06670ade72ee44e8c351cdc0e5a57af2fea21af AS build-base
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

FROM build-base AS api-build
COPY cmd/server/ ./cmd/server/
COPY cmd/migrate/ ./cmd/migrate/
COPY cmd/content-import/ ./cmd/content-import/
COPY cmd/home-v2-migrate/ ./cmd/home-v2-migrate/
COPY internal/ ./internal/
COPY seeds/ ./seeds/
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /hhc-web-api ./cmd/server \
 && CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /hhc-web-migrate ./cmd/migrate \
 && CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /hhc-web-content-import ./cmd/content-import \
 && CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /hhc-web-home-v2-migrate ./cmd/home-v2-migrate

FROM build-base AS worker-build
COPY cmd/bulletin-watermark-worker/ ./cmd/bulletin-watermark-worker/
# Explicit worker inputs keep API entrypoint changes out of this build cache.
COPY internal/auditclient/ ./internal/auditclient/
COPY internal/platform/ ./internal/platform/
COPY internal/assetclient/ ./internal/assetclient/
COPY internal/accountclient/ ./internal/accountclient/
COPY internal/bulletinwatermark/ ./internal/bulletinwatermark/
COPY internal/config/ ./internal/config/
COPY internal/logging/ ./internal/logging/
COPY internal/onlinebulletins/ ./internal/onlinebulletins/
COPY internal/bulletinreader/ ./internal/bulletinreader/
COPY internal/bulletins/ ./internal/bulletins/
COPY internal/sitesettings/ ./internal/sitesettings/
COPY internal/content/ ./internal/content/
COPY internal/diagnostics/ ./internal/diagnostics/
COPY internal/legal/ ./internal/legal/
COPY internal/translation/ ./internal/translation/
COPY internal/publication/ ./internal/publication/
COPY internal/recordings/ ./internal/recordings/
COPY internal/postgres/ ./internal/postgres/
RUN --mount=type=cache,target=/root/.cache/go-build CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /hhc-web-bulletin-worker ./cmd/bulletin-watermark-worker

FROM python@sha256:2fe5997d249a808b8eeea52c58a1dbffbba28754dc11699ef5c029f2d818ce79 AS watermark
# Refresh distribution security updates daily while retaining same-day build layers.
ARG APT_CACHE_DATE
RUN apt-get update && apt-get upgrade -y && apt-get install -y --no-install-recommends \
    poppler-utils=25.03.0-5+deb13u4 fontconfig=2.15.0-2.3 libglib2.0-0t64 libgomp1 zlib1g=1:1.3.dfsg+really1.3.1-1+deb13u1 \
    && rm -rf /var/lib/apt/lists/*
RUN python -m venv /opt/watermark
COPY tools/bulletin_watermark/requirements-linux.lock.txt /tmp/requirements.txt
ENV SOURCE_DATE_EPOCH=315532800
RUN python -c "from pathlib import Path; p=Path('/tmp/requirements.txt'); Path('/tmp/build-requirements.txt').write_text(''.join(l+'\\n' for l in p.read_text().splitlines() if l.lower().startswith('setuptools==')))" \
 && /opt/watermark/bin/pip install --no-cache-dir --require-hashes -r /tmp/build-requirements.txt \
 && /opt/watermark/bin/pip install --no-cache-dir --no-build-isolation --require-hashes -r /tmp/requirements.txt \
 && /opt/watermark/bin/pip check \
 && rm /tmp/requirements.txt /tmp/build-requirements.txt
COPY tools/bulletin_watermark/download_models.py /tmp/download-watermark-models.py
# Download, verify and set final model permissions in one layer. A separate
# chmod layer would copy the model bytes into the image a second time.
RUN python /tmp/download-watermark-models.py /opt/watermark/models \
 && rm /tmp/download-watermark-models.py \
 && printf '%s\n' \
    '43f37103f92efa8bd6b1c5902bb537cc12a981dc699fca19d9bb7de8c62d03d9  /opt/watermark/models/trustmark_Q.yaml' \
    'dc382c3f6b4fd568b27d6fbb763d6ffc2d2587126d84afe9d5ee95b4c5d99826  /opt/watermark/models/encoder_Q.ckpt' \
    'e3d9cea5406a26590735719f8f15cb10802b11852ae69047eaf4cf17214df781  /opt/watermark/models/decoder_Q.ckpt' \
    | sha256sum --check --strict - \
 && chmod 0444 /opt/watermark/models/* \
 && chmod 0555 /opt/watermark/models
RUN useradd --uid 65532 --no-create-home --shell /usr/sbin/nologin nonroot
COPY tools/bulletin_watermark/engine.py tools/bulletin_watermark/pdf_engine.py /opt/watermark/
COPY --chmod=0555 tools/bulletin_watermark/worker.py /hhc-web-bulletin-watermark
ENV PYTHONPATH=/opt/watermark PYTHONDONTWRITEBYTECODE=1 HHC_WATERMARK_MODEL_DIR=/opt/watermark/models HHC_WATERMARK_PDFTOPPM=/usr/bin/pdftoppm
COPY --from=worker-build /hhc-web-bulletin-worker /hhc-web-bulletin-worker
LABEL org.hhc.runtime="watermark-worker"
USER nonroot:nonroot
ENTRYPOINT ["/hhc-web-bulletin-worker"]

FROM gcr.io/distroless/static-debian12:nonroot@sha256:f5b485ea962d9bd1186b2f6b3a061191539b905b82ec395de78cbfae51f20e35 AS api
COPY --from=api-build /hhc-web-api /hhc-web-api
COPY --from=api-build /hhc-web-migrate /hhc-web-migrate
COPY --from=api-build /hhc-web-content-import /hhc-web-content-import
COPY --from=api-build /hhc-web-home-v2-migrate /hhc-web-home-v2-migrate
LABEL org.hhc.runtime="api"
ENV ACCESS_LOG_MODE=errors
EXPOSE 8082
USER nonroot:nonroot
ENTRYPOINT ["/hhc-web-api"]
