FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /waveform ./cmd/waveform

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source=https://github.com/ChadiDridi/waveform \
      org.opencontainers.image.description="Dependency-aware multi-service deploy planner and executor" \
      org.opencontainers.image.licenses=Apache-2.0
COPY --from=build /waveform /waveform
ENTRYPOINT ["/waveform"]
