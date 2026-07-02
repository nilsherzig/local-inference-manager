.PHONY: css build run test

# Rebuild the vendored Tailwind stylesheet from the templates.
css:
	tailwindcss -i internal/web/static/src.css -o internal/web/static/app.css --minify

build: css
	go build -o bin/lim ./cmd/lim

run: css
	go run ./cmd/lim -config config.yaml

test:
	go test ./...
