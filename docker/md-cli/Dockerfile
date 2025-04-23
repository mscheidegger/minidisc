# Build the Go binary.
FROM golang:1.23-alpine AS builder
WORKDIR /src
COPY go ./
RUN go mod download
WORKDIR /src/cmd/md
RUN go build -ldflags="-s -w" -o md

# Package the binary in a slim image
FROM gcr.io/distroless/static:latest
COPY --from=builder /src/cmd/md/md /usr/local/bin/md

# Default entrypoint
ENTRYPOINT ["md"]
CMD ["help"]
