FROM golang:1.25-bookworm AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o /topswatch ./cmd/topswatch

FROM gcr.io/distroless/static-debian12
COPY --from=builder /topswatch /topswatch
COPY topswatch.yaml /etc/topswatch/topswatch.yaml

EXPOSE 9876
ENTRYPOINT ["/topswatch", "--config", "/etc/topswatch/topswatch.yaml"]
