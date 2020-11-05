FROM golang:1.14 as builder
WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd
ENV CGO_ENABLED=0
RUN mkdir dist && \
  go build -o ./dist/dummy-cluster ./cmd/dummy-cluster/. && \
  go build -o ./dist/xds-service ./cmd/xds-service/.

FROM alpine:3.11

COPY --from=builder /app/dist /app/
