package exemenu

import (
	"flag"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWantJSON(t *testing.T) {
	tests := []struct {
		name     string
		flagFunc func() *flag.FlagSet
		args     []string
		want     bool
	}{
		{
			name:     "nil flagset",
			flagFunc: nil,
			args:     nil,
			want:     false,
		},
		{
			name: "json flag not set",
			flagFunc: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "output in JSON format")
				return fs
			},
			args: []string{},
			want: false,
		},
		{
			name: "json flag set",
			flagFunc: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "output in JSON format")
				return fs
			},
			args: []string{"--json"},
			want: true,
		},
		{
			name: "no json flag in flagset",
			flagFunc: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("other", false, "other flag")
				return fs
			},
			args: []string{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := &CommandContext{}
			if tt.flagFunc != nil {
				cc.FlagSet = tt.flagFunc()
				cc.FlagSet.Parse(tt.args)
			}
			assert.Equal(t, tt.want, cc.WantJSON())
		})
	}
}

func TestWantQR(t *testing.T) {
	tests := []struct {
		name     string
		flagFunc func() *flag.FlagSet
		args     []string
		want     bool
	}{
		{
			name:     "nil flagset",
			flagFunc: nil,
			args:     nil,
			want:     false,
		},
		{
			name: "qr flag not set",
			flagFunc: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("qr", false, "show QR code")
				return fs
			},
			args: []string{},
			want: false,
		},
		{
			name: "qr flag set",
			flagFunc: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("qr", false, "show QR code")
				return fs
			},
			args: []string{"--qr"},
			want: true,
		},
		{
			name: "no qr flag in flagset",
			flagFunc: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("other", false, "other flag")
				return fs
			},
			args: []string{},
			want: false,
		},
		{
			name: "qr and json flags together",
			flagFunc: func() *flag.FlagSet {
				fs := flag.NewFlagSet("test", flag.ContinueOnError)
				fs.Bool("json", false, "output in JSON format")
				fs.Bool("qr", false, "show QR code")
				return fs
			},
			args: []string{"--json", "--qr"},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := &CommandContext{}
			if tt.flagFunc != nil {
				cc.FlagSet = tt.flagFunc()
				cc.FlagSet.Parse(tt.args)
			}
			assert.Equal(t, tt.want, cc.WantQR())
		})
	}
}
