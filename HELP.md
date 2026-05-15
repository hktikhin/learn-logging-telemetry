# Install bootdev cli
go install github.com/bootdotdev/bootdev@latest

go build \
  -ldflags "-X boot.dev/linko/internal/build.GitSHA=$(git rev-parse HEAD) -X boot.dev/linko/internal/build.BuildTime=$(date -u '+%Y-%m-%dT%H:%M:%SZ')" \
  -o linko &&
LINKO_LOG_FILE=linko.access.log ENV=development ./linko

go get github.com/lmittmann/tint
go get github.com/mattn/go-isatty

go get gopkg.in/natefinch/lumberjack.v2