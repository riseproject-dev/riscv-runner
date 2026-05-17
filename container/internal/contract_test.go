package internal

import (
	"bytes"
	"log/slog"
	"math"
	"strings"
	"testing"
	"time"
)

func TestParseEntityType(t *testing.T) {
	cases := []struct {
		in      string
		want    EntityType
		wantErr bool
	}{
		{"Organization", EntityOrganization, false},
		{"User", EntityUser, false},
		{"", "", true},
		{"organization", "", true},
		{"Bot", "", true},
	}
	for _, c := range cases {
		got, err := ParseEntityType(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected err %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("%q: got %v want %v", c.in, got, c.want)
		}
	}
}

func TestEntity_LogValue(t *testing.T) {
	var buf bytes.Buffer
	h := slog.NewTextHandler(&buf, nil)
	slog.New(h).Info("hi", "entity", Entity{Type: EntityOrganization, Name: "acme", ID: 42})
	s := buf.String()
	if !strings.Contains(s, "entity.type=Organization") {
		t.Errorf("type missing: %q", s)
	}
	if !strings.Contains(s, "entity.name=acme") {
		t.Errorf("name missing: %q", s)
	}
	if !strings.Contains(s, "entity.id=42") {
		t.Errorf("id missing: %q", s)
	}
}

func TestJob_Entity(t *testing.T) {
	j := Job{EntityID: 7, EntityName: "acme", EntityType: "Organization"}
	e := j.Entity()
	if e.ID != 7 || e.Name != "acme" || e.Type != EntityOrganization {
		t.Errorf("got %+v", e)
	}
}

func TestWorker_Entity(t *testing.T) {
	w := Worker{EntityID: 9, EntityName: "luhenry", EntityType: "User"}
	e := w.Entity()
	if e.ID != 9 || e.Name != "luhenry" || e.Type != EntityUser {
		t.Errorf("got %+v", e)
	}
}

func TestGitHubAPIError_Error(t *testing.T) {
	e := &GitHubAPIError{StatusCode: 404, Message: "Not Found"}
	if e.Error() != "Not Found" {
		t.Errorf("got %q", e.Error())
	}
}

func TestAgeSeconds(t *testing.T) {
	// nil pointer → +Inf-ish
	if got := AgeSeconds(nil); got < 1e100 {
		t.Errorf("nil should be huge, got %v", got)
	}

	t0 := time.Now().Add(-30 * time.Second)
	got := AgeSeconds(&t0)
	if math.Abs(got-30) > 5 {
		t.Errorf("expected ~30, got %v", got)
	}
}
