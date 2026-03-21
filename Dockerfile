# Build stage
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /stile ./cmd/gateway/

# Runtime stage
FROM gcr.io/distroless/static
COPY --from=build /stile /stile
ENTRYPOINT ["/stile"]
CMD ["-config", "/etc/stile/config.yaml"]
