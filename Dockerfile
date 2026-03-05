FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy go.mod and go.sum (if it exists)
COPY go.mod ./
# Ignore error if go.sum doesn't exist yet
COPY go.sum* ./

RUN go mod download

COPY . .

# Build the binary named saucegen
RUN CGO_ENABLED=0 go build -o /saucegen main.go generator.go

FROM alpine:3.21

RUN apk add --no-cache ca-certificates

COPY --from=builder /saucegen /usr/local/bin/saucegen

# Ensure it's executable
RUN chmod +x /usr/local/bin/saucegen

ENTRYPOINT ["/usr/local/bin/saucegen"]
