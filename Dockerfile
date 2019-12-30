FROM golang as build

ADD . /src
RUN ["/bin/bash", "-exo", "pipefail", "-c", "cd /src; go generate; go build -o /dockerweb2 ."]


FROM buildpack-deps:scm

RUN ["mkdir", "/data"]
WORKDIR /data

COPY --from=build /dockerweb2 /dockerweb2
CMD ["/dockerweb2"]
