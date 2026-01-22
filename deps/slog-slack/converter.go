package slogslack

import (
	"log/slog"

	slogcommon "github.com/samber/slog-common"
	"github.com/slack-go/slack"
)

var SourceKey = "source"

type Converter func(addSource bool, replaceAttr func(groups []string, a slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record) *slack.WebhookMessage

func DefaultConverter(addSource bool, replaceAttr func(groups []string, a slog.Attr) slog.Attr, loggerAttr []slog.Attr, groups []string, record *slog.Record) *slack.WebhookMessage {
	// aggregate all attributes
	attrs := slogcommon.AppendRecordAttrsToAttrs(loggerAttr, groups, record)

	// developer formatters
	if addSource {
		attrs = append(attrs, slogcommon.Source(SourceKey, record))
	}
	attrs = slogcommon.ReplaceAttrs(replaceAttr, []string{}, attrs...)
	attrs = slogcommon.RemoveEmptyAttrs(attrs)

	// handler formatter
	message := &slack.WebhookMessage{}
	message.Text = record.Message
	message.Attachments = []slack.Attachment{
		{
			Color:  ColorMapping[record.Level],
			Fields: []slack.AttachmentField{},
		},
	}

	attrToSlackMessage("", attrs, message)
	return message
}

func attrToSlackMessage(base string, attrs []slog.Attr, message *slack.WebhookMessage) {
	for i := range attrs {
		attr := attrs[i]
		k := attr.Key
		v := attr.Value
		kind := attr.Value.Kind()

		if kind == slog.KindGroup {
			attrToSlackMessage(base+k+".", v.Group(), message)
		} else {
			field := slack.AttachmentField{}
			field.Title = base + k
			field.Value = slogcommon.ValueToString(v)
			message.Attachments[0].Fields = append(message.Attachments[0].Fields, field)
		}
	}
}
