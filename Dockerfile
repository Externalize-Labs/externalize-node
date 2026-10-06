FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /exnode ./cmd/exnode

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /exnode /exnode
ENV EXNODE_CACHE=/var/cache/exnode
VOLUME /var/cache/exnode
EXPOSE 8080
ENTRYPOINT ["/exnode"]
CMD ["serve"]
