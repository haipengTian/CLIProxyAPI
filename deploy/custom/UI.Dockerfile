ARG BUN_IMAGE=oven/bun:1.3.14
ARG NODE_IMAGE=node:24-bookworm-slim
FROM ${BUN_IMAGE} AS bun
FROM ${NODE_IMAGE} AS build
COPY --from=bun /usr/local/bin/bun /usr/local/bin/bun
WORKDIR /ui
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile
COPY . .
ARG VERSION=dev
ENV VERSION=${VERSION} LANG=en_US.UTF-8
RUN bun run verify && mv dist/index.html dist/management.html && cp LICENSE dist/LICENSE
FROM scratch AS artifact
COPY --from=build /ui/dist/ /
