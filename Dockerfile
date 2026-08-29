# Build.
FROM golang:1.27-alpine AS build
WORKDIR /src

# Dependencies first, so a source change does not re-download the module cache.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO off gives a static binary that runs on a scratch base and on distroless.
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jetwayd   ./cmd/jetwayd \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/jetwayctl ./cmd/jetwayctl \
 && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/carriersim ./cmd/carriersim

# Run.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/jetwayd   /usr/local/bin/jetwayd
COPY --from=build /out/jetwayctl /usr/local/bin/jetwayctl
COPY --from=build /out/carriersim /usr/local/bin/carriersim

# The spool must outlive the container. Mount a volume here in any deployment
# that enables it, or a restart discards messages that were acknowledged to a
# partner but not yet persisted.
VOLUME ["/var/lib/jetway"]

USER nonroot:nonroot
EXPOSE 8080 8443 9101
ENTRYPOINT ["/usr/local/bin/jetwayd"]
