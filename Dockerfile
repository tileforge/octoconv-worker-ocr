FROM golang:1.27.3-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/ocr-worker ./cmd/ocr-worker

FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      ca-certificates \
      tesseract-ocr \
      tesseract-ocr-eng \
      tesseract-ocr-rus \
      poppler-utils \
      fonts-dejavu-core \
 && mkdir -p /usr/local/lib /usr/local/share/tessdata \
 && cp -a /usr/share/tesseract-ocr/5/tessdata/. /usr/local/share/tessdata/ \
 && rm -rf /var/lib/apt/lists/*
ENV TESSDATA_PREFIX=/usr/local/share/tessdata
COPY --from=build /out/ocr-worker /usr/local/bin/ocr-worker
USER nobody
ENTRYPOINT ["/usr/local/bin/ocr-worker"]
