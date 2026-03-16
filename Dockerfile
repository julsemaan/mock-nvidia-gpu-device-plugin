FROM golang:1.26 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/mock-nvidia-device-plugin ./cmd/mock-nvidia-device-plugin

FROM gcr.io/distroless/static-debian12

COPY --from=builder /out/mock-nvidia-device-plugin /mock-nvidia-device-plugin

USER 0:0
ENTRYPOINT ["/mock-nvidia-device-plugin"]
