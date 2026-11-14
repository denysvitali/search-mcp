# Consumed by GoReleaser (dockers_v2): the build context holds the prebuilt
# binaries under <os>/<arch>/, so nothing is compiled here.
FROM gcr.io/distroless/static-debian12:nonroot

ARG TARGETPLATFORM
COPY ${TARGETPLATFORM}/search-mcp /usr/local/bin/search-mcp

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/search-mcp"]
CMD ["serve"]
