# Static binary on an empty image. Build the binary first:
#   CGO_ENABLED=0 go build -trimpath -o dsmr2homewizard ./cmd/dsmr2homewizard
FROM scratch
COPY dsmr2homewizard /dsmr2homewizard
ENTRYPOINT ["/dsmr2homewizard"]
