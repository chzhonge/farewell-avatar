FROM golang:1.23-alpine AS build
RUN apk add --no-cache ca-certificates
WORKDIR /src
COPY go.mod ./
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /countdown-avatar .

FROM scratch
COPY --from=build /countdown-avatar /countdown-avatar
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
ENV DATA_DIR=/data ADDR=0.0.0.0:8080
EXPOSE 8080
VOLUME /data
ENTRYPOINT ["/countdown-avatar"]
