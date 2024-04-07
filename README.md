# MPV Karaoke Web

MPV Karaoke Web is a web interface for queueing and playing karaoke songs with MPV. It is designed to be used on a local network, with the server running on the machine used for song
playback.

Use `--help` to see the available options.

## Build Dependencies
- Go 1.22
- Node, npm
- templ

## Runtime Dependencies
- mpv
- yt-dlp
- imagemagick

## Building
Use `make` to build the server. A static build will be output to `bin/mpvkaraoke`. Use Go's `GOOS` and `GOARCH` environment variables to cross-compile.

## Running
Run the server with `./bin/mpvkaraoke`. See `--help` for available options.

Example usage including required flags:
```sh
mpvkaraoke \
    --session-secret=SOME_SECRET \
    --client-secret=DISCORD_APPLICATION_SECRET \
    --client-id=DISCORD_APPLICATION_ID \
    --guild-id=DISCORD_GUILD_ID \
    --ngrok-domain=NGROK_DOMAIN \
    --ngrok-token=NGROK_TOKEN \
    --disable-cache
```