
# slog: Slack handler

[![tag](https://img.shields.io/github/tag/samber/slog-slack.svg)](https://github.com/samber/slog-slack/releases)
![Go Version](https://img.shields.io/badge/Go-%3E%3D%201.21-%23007d9c)
[![GoDoc](https://godoc.org/github.com/samber/slog-slack?status.svg)](https://pkg.go.dev/github.com/samber/slog-slack)
![Build Status](https://github.com/samber/slog-slack/actions/workflows/test.yml/badge.svg)
[![Go report](https://goreportcard.com/badge/github.com/samber/slog-slack)](https://goreportcard.com/report/github.com/samber/slog-slack)
[![Coverage](https://img.shields.io/codecov/c/github/samber/slog-slack)](https://codecov.io/gh/samber/slog-slack)
[![Contributors](https://img.shields.io/github/contributors/samber/slog-slack)](https://github.com/samber/slog-slack/graphs/contributors)
[![License](https://img.shields.io/github/license/samber/slog-slack)](./LICENSE)

A [Slack](https://slack.com) Handler for [slog](https://pkg.go.dev/golang.org/log/slog) Go library.

<div align="center">
  <hr>
  <sup><b>Sponsored by:</b></sup>
  <br>
  <a href="https://quickwit.io?utm_campaign=github_sponsorship&utm_medium=referral&utm_content=samber-slog-slack&utm_source=github">
    <div>
      <img src="https://github.com/samber/oops/assets/2951285/49aaaa2b-b8c6-4f21-909f-c12577bb6a2e" width="240" alt="Quickwit">
    </div>
    <div>
      Cloud-native search engine for observability - An OSS alternative to Splunk, Elasticsearch, Loki, and Tempo.
    </div>
  </a>
  <hr>
</div>

**See also:**

- [slog-multi](https://github.com/samber/slog-multi): `slog.Handler` chaining, fanout, routing, failover, load balancing...
- [slog-formatter](https://github.com/samber/slog-formatter): `slog` attribute formatting
- [slog-sampling](https://github.com/samber/slog-sampling): `slog` sampling policy
- [slog-mock](https://github.com/samber/slog-mock): `slog.Handler` for test purposes

**HTTP middlewares:**

- [slog-gin](https://github.com/samber/slog-gin): Gin middleware for `slog` logger
- [slog-echo](https://github.com/samber/slog-echo): Echo middleware for `slog` logger
- [slog-fiber](https://github.com/samber/slog-fiber): Fiber middleware for `slog` logger
- [slog-chi](https://github.com/samber/slog-chi): Chi middleware for `slog` logger
- [slog-http](https://github.com/samber/slog-http): `net/http` middleware for `slog` logger

**Loggers:**

- [slog-zap](https://github.com/samber/slog-zap): A `slog` handler for `Zap`
- [slog-zerolog](https://github.com/samber/slog-zerolog): A `slog` handler for `Zerolog`
- [slog-logrus](https://github.com/samber/slog-logrus): A `slog` handler for `Logrus`

**Log sinks:**

- [slog-datadog](https://github.com/samber/slog-datadog): A `slog` handler for `Datadog`
- [slog-betterstack](https://github.com/samber/slog-betterstack): A `slog` handler for `Betterstack`
- [slog-rollbar](https://github.com/samber/slog-rollbar): A `slog` handler for `Rollbar`
- [slog-loki](https://github.com/samber/slog-loki): A `slog` handler for `Loki`
- [slog-sentry](https://github.com/samber/slog-sentry): A `slog` handler for `Sentry`
- [slog-syslog](https://github.com/samber/slog-syslog): A `slog` handler for `Syslog`
- [slog-logstash](https://github.com/samber/slog-logstash): A `slog` handler for `Logstash`
- [slog-fluentd](https://github.com/samber/slog-fluentd): A `slog` handler for `Fluentd`
- [slog-graylog](https://github.com/samber/slog-graylog): A `slog` handler for `Graylog`
- [slog-quickwit](https://github.com/samber/slog-quickwit): A `slog` handler for `Quickwit`
- [slog-slack](https://github.com/samber/slog-slack): A `slog` handler for `Slack`
- [slog-telegram](https://github.com/samber/slog-telegram): A `slog` handler for `Telegram`
- [slog-mattermost](https://github.com/samber/slog-mattermost): A `slog` handler for `Mattermost`
- [slog-microsoft-teams](https://github.com/samber/slog-microsoft-teams): A `slog` handler for `Microsoft Teams`
- [slog-webhook](https://github.com/samber/slog-webhook): A `slog` handler for `Webhook`
- [slog-kafka](https://github.com/samber/slog-kafka): A `slog` handler for `Kafka`
- [slog-nats](https://github.com/samber/slog-nats): A `slog` handler for `NATS`
- [slog-parquet](https://github.com/samber/slog-parquet): A `slog` handler for `Parquet` + `Object Storage`
- [slog-channel](https://github.com/samber/slog-channel): A `slog` handler for Go channels

## 🚀 Install

```sh
go get github.com/samber/slog-slack/v2
```

**Compatibility**: go >= 1.21

No breaking changes will be made to exported APIs before v3.0.0.

## 💡 Usage

GoDoc: [https://pkg.go.dev/github.com/samber/slog-slack/v2](https://pkg.go.dev/github.com/samber/slog-slack/v2)

### Handler options

```go
type Option struct {
    // log level (default: debug)
    Level slog.Leveler

    // slack webhook url
    WebhookURL string
    // slack bot token
    BotToken string
    // slack channel (default: webhook channel)
    Channel string
    // bot username (default: webhook username)
    Username string
    // bot emoji (default: webhook emoji)
    IconEmoji string
    // bot emoji (default: webhook emoji)
    IconURL string
    // Not implemented yet, but we would like your feedback here: #7
    // ThreadTimestamp string

    // API request timeout (default: 10s)
    Timeout time.Duration

    // optional: customize Slack message builder
    Converter Converter

    // optional: see slog.HandlerOptions
    AddSource   bool
    ReplaceAttr func(groups []string, a slog.Attr) slog.Attr
}
```

Attributes will be injected in message attachments.

Other global parameters:

```go
slogslack.SourceKey = "source"
slogslack.ColorMapping = map[slog.Level]string{...}
```

![screenshot](./screenshot.png)

### Example

#### Using webhook

Generate a webhook [here](https://slack.com/apps/A0F7XDUAZ-incoming-webhooks).

```go
import (
    slogslack "github.com/samber/slog-slack/v2"
    "log/slog"
)

func main() {
    webhook := "https://hooks.slack.com/services/xxx/yyy/zzz"
    channel := "alerts"

    logger := slog.New(slogslack.Option{Level: slog.LevelError, WebhookURL: webhook, Channel: channel}.NewSlackHandler())
    logger = logger.
        With("environment", "dev").
        With("release", "v1.0.0")

    // log error
    logger.
        With("category", "sql").
        With("query.statement", "SELECT COUNT(*) FROM users;").
        With("query.duration", 1*time.Second).
        With("error", fmt.Errorf("could not count users")).
        Error("caramba!")

    // log user signup
    logger.
        With(
            slog.Group("user",
                slog.String("id", "user-123"),
                slog.Time("created_at", time.Now()),
            ),
        ).
        Info("user registration")

    // push record to a thread
    logger.ErrorContext(
        slogslack.WithThreadTimestamp(context.Background(), "1714929099.4238"),
        "An error",
    )
}
```

#### Using bot token

Use [Bot token](https://api.slack.com/authentication/token-types#bot).

```go
import (
	slogslack "github.com/samber/slog-slack/v2"
	"log/slog"
)

func main() {
    token := "xoxb-"
    channel := "alerts"

    logger := slog.New(slogslack.Option{Level: slog.LevelError, BotToken: token, Channel: channel}.NewSlackHandler())
    logger = logger.
        With("environment", "dev").
        With("release", "v1.0.0")
}
```

## 🤝 Contributing

- Ping me on twitter [@samuelberthe](https://twitter.com/samuelberthe) (DMs, mentions, whatever :))
- Fork the [project](https://github.com/samber/slog-slack)
- Fix [open issues](https://github.com/samber/slog-slack/issues) or request new features

Don't hesitate ;)

```bash
# Install some dev dependencies
make tools

# Run tests
make test
# or
make watch-test
```

## 👤 Contributors

![Contributors](https://contrib.rocks/image?repo=samber/slog-slack)

## 💫 Show your support

Give a ⭐️ if this project helped you!

[![GitHub Sponsors](https://img.shields.io/github/sponsors/samber?style=for-the-badge)](https://github.com/sponsors/samber)

## 📝 License

Copyright © 2023 [Samuel Berthe](https://github.com/samber).

This project is [MIT](./LICENSE) licensed.
