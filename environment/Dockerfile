FROM golang:1.25.1-bookworm@sha256:c423747fbd96fd8f0b1102d947f51f9b266060217478e5f9bf86f145969562ee

WORKDIR /app

ENV GOTOOLCHAIN=local \
    CGO_ENABLED=1

COPY . /app/

RUN groupadd --gid 10000 agent \
    && groupadd --gid 10001 verifier \
    && useradd --create-home --uid 10000 --gid 10000 agent \
    && useradd --create-home --uid 10001 --gid 10001 verifier \
    && go mod download \
    && mkdir -p /home/agent/.cache/go-build /home/verifier/.cache/go-build /logs/verifier \
    && chown -R agent:agent /home/agent \
    && chown -R verifier:verifier /home/verifier /logs/verifier \
    && chown -R agent:verifier /app \
    && chmod -R u+rwX,g+rwX,o+rX /app \
    && find /app -type d -exec chmod g+s {} + \
    && chmod 0700 /logs/verifier \
    && chmod -R go-w /usr/local/go /go/pkg/mod
