package execore

import (
	"context"
	"reflect"
	"testing"

	"exe.dev/exedb"
)

func TestParseReflectionFields(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
		err  bool
	}{
		{"empty", "", nil, false},
		{"none", "none", nil, false},
		{"all", "all", []string{"all"}, false},
		{"all-with-others", "email,all,tags", []string{"all"}, false},
		{"explicit", "email,tags", []string{"email", "tags"}, false},
		{"dedup-and-sort", "tags,email,tags", []string{"email", "tags"}, false},
		{"unknown", "bogus", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseReflectionFields(c.in)
			if (err != nil) != c.err {
				t.Fatalf("err=%v wantErr=%v", err, c.err)
			}
			if !c.err && !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestReflectionFieldEnabled(t *testing.T) {
	if !reflectionFieldEnabled([]string{"all"}, "email") {
		t.Errorf("all should enable email")
	}
	// "all" should also enable any future field name.
	if !reflectionFieldEnabled([]string{"all"}, "future-field-not-yet-defined") {
		t.Errorf("all should enable an unknown future field")
	}
	if reflectionFieldEnabled([]string{"email"}, "tags") {
		t.Errorf("explicit list should not enable absent field")
	}
	if !reflectionFieldEnabled([]string{"email"}, "email") {
		t.Errorf("explicit list should enable listed field")
	}
	if reflectionFieldEnabled(nil, "email") {
		t.Errorf("nil fields should disable everything")
	}
}

func TestDefaultReflectionIntegration(t *testing.T) {
	cfgJSON, err := defaultReflectionIntegrationConfigJSON()
	if err != nil {
		t.Fatalf("defaultReflectionIntegrationConfigJSON: %v", err)
	}
	ig := exedb.Integration{
		OwnerUserID: "usr_test",
		Type:        "reflection",
		Name:        "reflection",
		Config:      cfgJSON,
		Attachments: exedb.AttachmentsJSON([]string{"auto:all"}),
		Comment:     defaultReflectionIntegrationComment,
	}
	if !isDefaultReflectionIntegration(ig) {
		t.Fatalf("expected default reflection integration to match")
	}
	if !exedb.IntegrationMatchesBox(&ig, &exedb.Box{Name: "dev", CreatedByUserID: "usr_test"}) {
		t.Fatalf("auto:all should match any VM")
	}

	ig.Attachments = exedb.AttachmentsJSON([]string{"vm:dev"})
	if !isDefaultReflectionIntegration(ig) {
		t.Fatalf("default flair should not depend on attachments")
	}

	ig.Comment = "custom"
	if isDefaultReflectionIntegration(ig) {
		t.Fatalf("custom comment should not receive the default flair")
	}
}

func TestInstallDefaultReflectionIntegrationIdempotent(t *testing.T) {
	server := newTestServer(t)
	ctx := context.Background()
	userID := "usr_defaultreflect"

	err := server.withTx(ctx, func(ctx context.Context, q *exedb.Queries) error {
		if err := q.InsertUser(ctx, exedb.InsertUserParams{
			UserID: userID,
			Email:  "default-reflect@example.com",
			Region: "lax",
		}); err != nil {
			return err
		}
		if err := installDefaultReflectionIntegration(ctx, q, userID); err != nil {
			return err
		}
		return installDefaultReflectionIntegration(ctx, q, userID)
	})
	if err != nil {
		t.Fatalf("installDefaultReflectionIntegration: %v", err)
	}

	ints, err := withRxRes1(server, ctx, (*exedb.Queries).ListIntegrationsByUser, userID)
	if err != nil {
		t.Fatalf("ListIntegrationsByUser: %v", err)
	}
	if len(ints) != 1 || !isDefaultReflectionIntegration(ints[0]) {
		t.Fatalf("integrations = %#v, want exactly default reflection integration", ints)
	}
}
