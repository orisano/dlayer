FROM golang:1.17 AS builder

WORKDIR /go/src/app
COPY . .

ENV CGO_ENABLED=0 GOOS=linux

RUN go install -v ./...

FROM scratch

COPY --from=builder /go/bin/dlayer /bin/dlayer

ENTRYPOINT ["/bin/dlayer"]
