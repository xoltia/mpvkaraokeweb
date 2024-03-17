build: templates tailwind
	go build -o ./bin/mpvkaraoke ./cmd

templates:
	templ generate

tailwind:
	npx tailwindcss -i ./style/base.css -o ./style/style.css

run: build
	./bin/mpvkaraoke