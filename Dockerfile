FROM golang:1.24-alpine AS build
# Refresh the CA bundle so the runtime image does not inherit the eMudhra
# roots (distrusted 2026-10-20). distroless/static ships its own bundle and
# had not been rebuilt with the fix, so we copy this one over it below.
# Upgrade both packages: ca-certificates' trigger regenerates the bundle
# from its own cert list, so upgrading only ca-certificates-bundle is undone.
RUN apk add --no-cache --upgrade ca-certificates ca-certificates-bundle
WORKDIR /src
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/pingmcp .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/pingmcp /pingmcp
EXPOSE 8100
USER nonroot:nonroot
ENTRYPOINT ["/pingmcp"]
