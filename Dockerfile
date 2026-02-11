# Build stage
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /app ./cmd/server

# Final stage
FROM alpine:3.19
RUN apk add --no-cache ca-certificates
COPY --from=build /app /app
EXPOSE 8080
ENTRYPOINT ["/app"]
