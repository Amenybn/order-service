# order-service : Go (binaire statique, aucune dépendance externe)
FROM golang:1.23-alpine AS build
ENV CGO_ENABLED=0
WORKDIR /src
COPY go.mod ./
COPY *.go ./
# Les tests sont exécutés pendant le build : une image n'est produite que si ils passent
RUN go vet ./... && go test ./... && go build -ldflags="-s -w" -o /out/order-service .

FROM alpine:3.20
COPY --from=build /out/order-service /app/order-service

ENV PORT=8002 \
    CATALOG_URL=http://catalog-service:8001
# Non-root numérique, aucune écriture disque
USER 65532:65532
EXPOSE 8002

HEALTHCHECK --interval=15s --timeout=5s --start-period=5s --retries=3 CMD ["/app/order-service", "healthcheck"]

ENTRYPOINT ["/app/order-service"]
