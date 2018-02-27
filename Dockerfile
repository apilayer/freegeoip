FROM golang:1.9-alpine

RUN apk add --no-cache git libcap shadow
RUN useradd -ms /bin/bash freegeoip
COPY cmd/freegeoip/public /var/www
ADD . /go/src/github.com/apilayer/freegeoip
WORKDIR /go/src/github.com/apilayer/freegeoip/cmd/freegeoip
RUN \
      go get -d && \
      go install && \
      setcap cap_net_bind_service=+ep /go/bin/freegeoip

ENTRYPOINT ["/go/bin/freegeoip"]
USER freegeoip

EXPOSE 8080
