package slogslack

import (
	"log/slog"
)

var ColorMapping = map[slog.Level]string{
	slog.LevelDebug: "#63C5DA",
	slog.LevelInfo:  "#63C5DA",
	slog.LevelWarn:  "#FFA500",
	slog.LevelError: "#FF0000",
}
