FROM golang:1.9-alpine

COPY cmd/freegeoip/public /var/www

ADD . /go/src/github.com/apilayer/freegeoip
WORKDIR /go/src/github.com/apilayer/freegeoip/cmd/freegeoip
RUN apk update
RUN apk add git libcap shadow
RUN go get -d
RUN go install
RUN setcap cap_net_bind_service=+ep /go/bin/freegeoip
RUN useradd -ms /bin/bash freegeoip

USER freegeoip
ENTRYPOINT ["/go/bin/freegeoip"]

EXPOSE 8080

# CMD instructions:
# Add  "-use-x-forwarded-for"      if your server is behind a reverse proxy
# Add  "-public", "/var/www"       to enable the web front-end
# Add  "-internal-server", "8888"  to enable the pprof+metrics server
#
# Example:
# CMD ["-use-x-forwarded-for", "-public", "/var/www", "-internal-server", "8888"]
