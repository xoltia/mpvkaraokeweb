# MPV Karaoke Web

MPV Karaoke Web is a web interface for queueing and playing karaoke songs with MPV. It is designed to be used on a local network, with the server running on the machine used for song
playback.

Use `--help` to see the available options.


## Building
Use `make` to build the server. A static build will be output to `bin/mpvkaraoke`. Use Go's `GOOS` and `GOARCH` environment variables to cross-compile.

### Dependencies
- Go 1.22
- Node, npm
- templ

## Running
Run the server with `./bin/mpvkaraoke`. See `--help` for available options.

### Dependencies
- mpv
- yt-dlp
- imagemagick



### Setup
1. Create an account on [ngrok](https://ngrok.com/) and get your auth token.
2. Claim a domain on ngrok.
3. Create a Discord application on the [Discord Developer Portal](https://discord.com/developers/applications).
4. Under OAuth2, add a redirect URL to `https://NGROK_DOMAIN/auth/callback`.
5. Copy the client ID and secret from the Discord application.
6. Run the server with the required flags.

### Example
This command will start the application with the required flags, a maximum user queue of 3, and disable preemptive video caching.
```sh
mpvkaraoke \
    --session-secret=SOME_SECRET \
    --client-secret=DISCORD_APPLICATION_SECRET \
    --client-id=DISCORD_APPLICATION_ID \
    --guild-id=DISCORD_GUILD_ID \
    --ngrok-domain=NGROK_DOMAIN \
    --ngrok-token=NGROK_TOKEN \
    --max-queue=3 \
    --disable-cache
```
