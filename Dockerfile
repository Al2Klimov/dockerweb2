FROM golang as build

ADD . /src
RUN ["/bin/bash", "-exo", "pipefail", "-c", "cd /src; go generate; go build -o /dockerweb2 ."]


FROM debian:testing
SHELL ["/bin/bash", "-exo", "pipefail", "-c"]

RUN apt-get update ;\
        DEBIAN_FRONTEND=noninteractive apt-get install --no-install-{recommends,suggests} -y \
                ca-certificates git openssh-client s-nail ;\
        apt-get clean ;\
        rm -vrf /var/lib/apt/lists/*

COPY --from=build /dockerweb2 /dockerweb2

RUN mkdir /data ;\
	rm -rf ~ ;\
	ln -vs /data ~

WORKDIR /data

CMD ["/dockerweb2"]
