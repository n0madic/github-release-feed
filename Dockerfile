FROM golang:alpine AS builder

RUN apk add --quiet --no-cache build-base git

WORKDIR /src

ADD go.* ./

RUN go mod download

ADD . .

RUN go install -ldflags="-linkmode external -extldflags '-static' -s -w"


FROM scratch

COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /go/bin/* /

CMD ["/github-release-feed"]
