# Build stage
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /stile ./cmd/gateway/
RUN CGO_ENABLED=0 go build -o /mock-oauth-provider ./scripts/mock-oauth-provider.go
RUN CGO_ENABLED=0 go build -o /mock-oauth-upstream ./scripts/mock-oauth-upstream.go

# Runtime stage
FROM gcr.io/distroless/static
COPY --from=build /stile /stile
COPY --from=build /mock-oauth-provider /mock-oauth-provider
COPY --from=build /mock-oauth-upstream /mock-oauth-upstream
ENTRYPOINT ["/stile"]
CMD ["-config", "/etc/stile/config.yaml"]
