FROM golang:1.25.1-bookworm@sha256:c423747fbd96fd8f0b1102d947f51f9b266060217478e5f9bf86f145969562ee

WORKDIR /app

ENV GOTOOLCHAIN=local \
    CGO_ENABLED=1

COPY . /app/

RUN useradd --create-home --uid 10001 verifier \
    && go mod download \
    && mkdir -p /logs/verifier \
    && chown -R verifier:verifier /app /logs/verifier
