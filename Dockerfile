# minmaxmusic3-web — Go + html/template + htmx front-end for MiniMax Music 3.
#
# Stage layout follows the timbre pattern: toolchain → tailwind build →
# alpine secretbase (Infisical CLI + entrypoint) → runtime. The app listens
# on :8080 and is EXPOSEd only — NPM reaches it by service name on
# shared_net; no host ports are ever published.

# --- test: run the suite -------------------------------------------------
FROM golang:1.26-alpine@sha256:70b46548e42db77e0966aaf3619fd068734dc6c77584d526b91126504fd95816 AS test
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go test ./...

# --- build: tests are a hard dependency, then compile app + CSS ----------
# FROM test means a runtime image cannot be produced unless the suite passed.
FROM test AS build
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/mm3-server ./cmd/server

# Tailwind v4 CLI builds app.css from input.css (unminified so color-mix()
# stays symbolic — see stages/03 output; a minifier folds mixes into foreign
# hexes and breaks the exhaustive-palette check).
RUN apk add --no-cache nodejs npm && \
    npm install --no-save tailwindcss@4 @tailwindcss/cli@4 && \
    npx @tailwindcss/cli -i web/static/input.css -o /out/app.css

# --- Infisical CLI, pinned by immutable digest ---------------------------
# Copy the binary from the vendor image rather than curl|sh at build time.
# Digest verified locally on 2026-08-14; upgrades are explicit diffs.
FROM infisical/cli@sha256:4fd22fff5878e9313e824ec7360b065c546fe5172f4c641e91220e769fb4687a AS infisical-cli

# --- secretbase: alpine + CA certs + pinned Infisical CLI + entrypoint ---
# Alpine rather than distroless because the entrypoint needs a shell to
# mint an Infisical token before exec'ing the process it wraps.
FROM alpine:3.21 AS secretbase
RUN apk add --no-cache ca-certificates tzdata wget
COPY --from=infisical-cli /bin/infisical /usr/local/bin/infisical
COPY docker/entrypoint.sh /usr/local/bin/entrypoint.sh
RUN chmod 0755 /usr/local/bin/entrypoint.sh
ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]

# --- runtime --------------------------------------------------------------
FROM secretbase AS runtime
WORKDIR /app
RUN addgroup -S mm3 && adduser -S mm3 -G mm3 && mkdir -p /data && chown -R mm3:mm3 /data
COPY --from=build --chown=mm3:mm3 /out/mm3-server /usr/local/bin/mm3-server
COPY --chown=mm3:mm3 web/templates /app/web/templates
COPY --chown=mm3:mm3 web/static /app/web/static
# Copy the CSS build LAST so a stale source-tree app.css cannot overwrite it.
COPY --from=build --chown=mm3:mm3 /out/app.css /app/web/static/app.css
COPY --chown=mm3:mm3 shared /app/shared
USER mm3
ENV MM3_ADDR=":8080" MM3_WEB_DIR="/app/web" \
    MM3_DB_PATH="/data/mm3.db" MM3_AUDIO_DIR="/data/audio"
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s \
    CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
CMD ["/usr/local/bin/mm3-server"]
