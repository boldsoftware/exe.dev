package cobble

import (
	"context"
	"testing"

	"golang.org/x/crypto/acme"
)

func TestSmoke(t *testing.T) {
	ctx := context.Background()
	stone, err := Start(ctx, &Config{
		Log: t.Output(),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer stone.Stop()

	client := stone.Client()
	account := &acme.Account{
		Contact: []string{"mailto:blake@exe.dev"},
	}
	account, err = client.Register(ctx, account, acme.AcceptTOS)
	if err != nil {
		t.Fatal(err)
	}
	if account.Status != acme.StatusValid {
		t.Fatalf("expected account status to be valid, got %q", account.Status)
	}
}
