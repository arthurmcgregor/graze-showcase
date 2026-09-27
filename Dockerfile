FROM golang:1.26-alpine AS build
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -o graze .

FROM alpine:latest
# Timestamps are rendered in the reader's timezone, which needs the zone files.
RUN apk add --no-cache tzdata
WORKDIR /app
COPY --from=build /app/graze .
COPY templates/ templates/
COPY static/ static/
EXPOSE 8097
CMD ["./graze"]
