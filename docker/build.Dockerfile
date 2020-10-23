FROM golang:1.12-stretch as builder

ENV CGO_LDFLAGS_ALLOW="-fopenmp"
ENV GO111MODULE="on"

WORKDIR /app
COPY . /app
RUN apt-get update > /dev/null && apt-get install -y libglib2.0-dev curl make > /dev/null
RUN make build_init
RUN make build_backend
RUN chmod -R o+r+w+x ./dist/data
