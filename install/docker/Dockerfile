FROM golang:1.24 AS meshery-server
ARG TOKEN
ARG GIT_VERSION
ARG GIT_COMMITSHA
ARG RELEASE_CHANNEL

RUN adduser --disabled-login appuser
WORKDIR /github.com/meshery/meshery
ADD . .
RUN go clean -modcache; cd server; cd cmd; GOPROXY=https://proxy.golang.org GOSUMDB=off go build -ldflags="-w -s -X main.globalTokenForAnonymousResults=$TOKEN -X main.version=$GIT_VERSION -X main.commitsha=$GIT_COMMITSHA -X main.releasechannel=$RELEASE_CHANNEL" -tags draft -a -o /meshery .

FROM node:20-alpine AS base-node

# Installing NodeJS Dependencies
FROM base-node AS meshery-ui-deps
RUN apk add --no-cache libc6-compat
WORKDIR /app
COPY ui/package.json ui/package-lock.json ./
ENV HUSKY=0
RUN npm ci --omit=dev --legacy-peer-deps --ignore-scripts

FROM base-node AS meshery-ui-provider-deps
RUN apk add --no-cache libc6-compat
WORKDIR /app
COPY provider-ui/package.json provider-ui/package-lock.json ./
RUN npm ci --omit=dev --legacy-peer-deps

# Build NextJS Application
FROM base-node AS meshery-ui-builder
WORKDIR /app
COPY --from=meshery-ui-deps /app/node_modules ./node_modules
COPY ui .
# Disable NextJS Telemetry data collect
ENV NEXT_TELEMETRY_DISABLED=1
RUN npm run build && npm run export

FROM base-node AS meshery-ui-provider-builder
WORKDIR /app
COPY --from=meshery-ui-provider-deps /app/node_modules ./node_modules
# Disable NextJS Telemetry data collect
ENV NEXT_TELEMETRY_DISABLED=1
COPY provider-ui .
RUN npm run build

# FROM ubuntu AS wrk2
# RUN apt-get -y update && apt-get -y install git && apt-get clean && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* /usr/share/man/?? /usr/share/man/??_*
# RUN apt-get -y update && apt-get -y  install build-essential libssl-dev git zlib1g-dev
# RUN git config --global user.email "meshery@meshery.io"
# RUN git config --global user.name "meshery"
# RUN git clone --depth=1 https://github.com/layer5io/wrk2 && cd wrk2 && make

FROM alpine:3.21.3 AS seed_content
RUN apk add --no-cache curl
WORKDIR /
RUN lines=$(curl -s https://api.github.com/repos/layer5io/wasm-filters/releases/latest | grep "browser_download_url.*wasm" | cut -d : -f 2,3 | sed 's/"//g') \
    && mkdir -p seed_content/filters/binaries \
    && cd seed_content/filters/binaries  \
    for line in $lines \
    do \
    curl -LO $line \
    done
# Antiquated patterns; no longer compatible with the current version of Meshery
# RUN curl -L -s https://github.com/service-mesh-patterns/service-mesh-patterns/tarball/master -o service-mesh-patterns.tgz \
#     && mkdir service-mesh-patterns \
#     && mkdir -p /seed_content/patterns \
#     && tar xzf service-mesh-patterns.tgz --directory=service-mesh-patterns \
#     && mv service-mesh-patterns/*/samples/* /seed_content/patterns/

#FROM ubuntu AS nighthawk
#RUN apt-get -y update && apt-get -y install git && apt-get clean && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/* /usr/share/man/?? /usr/share/man/??_*
#RUN apt-get -y update && apt-get -y  install build-essential libssl-dev git zlib1g-dev
#RUN git config --global user.email "ci@meshery.io"
#RUN git config --global user.name "meshery"
#RUN git clone https://github.com/layer5io/nighthawk-go
#RUN cd nighthawk-go/apinighthawk/bin && chmod +x ./nighthawk_client


FROM alpine:3.21.3
ENV GLIBC_REPO=https://github.com/sgerrand/alpine-pkg-glibc/releases/download
ENV GLIBC_VERSION=2.35-r1
RUN set -ex && \
    apk --update add libstdc++ curl ca-certificates && \
    apk add --no-cache --virtual .build-deps curl binutils zstd gcompat && \
    for pkg in glibc-${GLIBC_VERSION} glibc-bin-${GLIBC_VERSION}; \
    do curl -sSL ${GLIBC_REPO}/${GLIBC_VERSION}/${pkg}.apk -o /tmp/${pkg}.apk; done && \
    apk del gcompat && \
    apk add --force-overwrite --no-cache --allow-untrusted /tmp/*.apk && \
    rm -v /tmp/*.apk && \
    /usr/glibc-compat/sbin/ldconfig /lib /usr/glibc-compat/lib

RUN update-ca-certificates

COPY ./server/cmd/server-config.env /app/server/cmd/server-config.env
COPY ./server/meshmodel /app/server/meshmodel
COPY ./server/permissions /app/server/permissions/
COPY ./docs/catalog /app/docs/catalog/
COPY --from=meshery-server /meshery /app/server/cmd/
COPY --from=meshery-server /etc/passwd /etc/passwd
COPY --from=meshery-server /github.com/meshery/meshery/server/helpers/swagger.yaml /app/server/helpers/swagger.yaml
COPY --from=meshery-ui-builder /app/out /app/ui/out
COPY --from=meshery-ui-builder /app/public /app/ui/public
COPY --from=meshery-ui-provider-builder /app/out /app/provider-ui/out
# COPY --from=wrk2 /wrk2 /app/server/cmd/wrk2
# COPY --from=wrk2 /wrk2/wrk /usr/local/bin
COPY --from=seed_content /seed_content /home/appuser/.meshery/seed_content
COPY --from=layer5/getnighthawk:v1.0.1 /usr/local/bin/nighthawk_service /app/server/cmd/
COPY --from=layer5/getnighthawk:v1.0.1 /usr/local/bin/nighthawk_output_transform /app/server/cmd/

# Permission keys are authorization keys which server will
# seed on initial boot for local provider, if there is update on this path
# ensure to update here also https://github.com/meshery/meshery/blob/85a0fd9b9cc03bbc51c4aa1efd1e837db72ef691/install/kubernetes/helm/meshery/values.yaml#L19
ENV KEYS_PATH="/app/server/permissions/keys.csv"

RUN addgroup docker
RUN mkdir -p /var/run; chown -R appuser:docker /var/run/
RUN mkdir -p /home/appuser/.meshery/config; chown -R appuser /home/appuser/; chown -R appuser /app/ui/public
USER appuser
WORKDIR /app/server/cmd
CMD ["./meshery"]
