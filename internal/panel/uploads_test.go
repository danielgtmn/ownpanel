package panel

import (
	"context"
	"testing"
	"time"
)

// tokenActor creates a real write token for the test admin, so upload links
// can re-check it when they are used.
func tokenActor(t *testing.T, svc *Service, name string) (Actor, string) {
	t.Helper()
	ctx := context.Background()
	nt, err := svc.CreateToken(ctx, admin, name, "write", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	a, err := svc.TokenActor(ctx, nt.Secret)
	if err != nil {
		t.Fatal(err)
	}
	return a, nt.ID
}

func TestUploadLinkSingleUseAndExpiry(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	clock := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	svc.now = func() time.Time { return clock }
	if _, err := svc.SaveSite(ctx, admin, "blog", []string{"blog.example.com"}); err != nil {
		t.Fatal(err)
	}
	creator, _ := tokenActor(t, svc, "ci")

	link, err := svc.CreateUploadLink(ctx, creator, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if want := clock.Add(15 * time.Minute); !link.ExpiresAt.Equal(want) {
		t.Fatalf("expires %v, want %v", link.ExpiresAt, want)
	}
	// Unused, a link survives until shortly before the deadline.
	clock = clock.Add(14*time.Minute + 59*time.Second)
	if _, err := svc.PublishWithUploadLink(ctx, link.ID, tarGz(t, map[string]string{"index.html": "one"})); err != nil {
		t.Fatalf("upload before expiry: %v", err)
	}
	_, err = svc.PublishWithUploadLink(ctx, link.ID, tarGz(t, map[string]string{"index.html": "two"}))
	wantKind(t, err, NotFound)

	// Expired links are refused, and used up by the attempt.
	link, err = svc.CreateUploadLink(ctx, creator, "blog")
	if err != nil {
		t.Fatal(err)
	}
	clock = clock.Add(15 * time.Minute)
	_, err = svc.PublishWithUploadLink(ctx, link.ID, tarGz(t, map[string]string{"index.html": "late"}))
	wantKind(t, err, NotFound)
	clock = clock.Add(-10 * time.Minute)
	_, err = svc.PublishWithUploadLink(ctx, link.ID, tarGz(t, map[string]string{"index.html": "late"}))
	wantKind(t, err, NotFound)

	_, err = svc.PublishWithUploadLink(ctx, "unknown", tarGz(t, map[string]string{"index.html": "x"}))
	wantKind(t, err, NotFound)
}

func TestUploadLinkRequiresWriteAccessToTheSite(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", nil); err != nil {
		t.Fatal(err)
	}
	reader := Actor{UserID: "1", Name: "root", Admin: true, ReadOnly: true, Via: ViaToken, tokenName: "r"}
	_, err := svc.CreateUploadLink(ctx, reader, "blog")
	wantKind(t, err, Forbidden)
	other := Actor{UserID: "1", Name: "root", Admin: true, Sites: []string{"x"}, Via: ViaToken, tokenName: "o"}
	_, err = svc.CreateUploadLink(ctx, other, "blog")
	wantKind(t, err, NotFound)
	_, err = svc.CreateUploadLink(ctx, Actor{}, "blog")
	wantKind(t, err, Unauthenticated)
}

func TestUploadLinkPublishesAsItsCreator(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", nil); err != nil {
		t.Fatal(err)
	}
	tok, _ := tokenActor(t, svc, "ci")
	link, err := svc.CreateUploadLink(ctx, tok, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PublishWithUploadLink(ctx, link.ID, tarGz(t, map[string]string{"index.html": "x"})); err != nil {
		t.Fatal(err)
	}
	acts, err := svc.Activities(ctx, admin, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range acts {
		if a.Action == "site.publish" {
			if a.Actor != "root via token ci" {
				t.Fatalf("actor %q", a.Actor)
			}
			return
		}
	}
	t.Fatal("no publish in the activity log")
}

func TestUploadLinkLimitPerUser(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxOpenUploadLinks; i++ {
		if _, err := svc.CreateUploadLink(ctx, admin, "blog"); err != nil {
			t.Fatal(err)
		}
	}
	_, err := svc.CreateUploadLink(ctx, admin, "blog")
	wantKind(t, err, Conflict)
}

func TestUploadLinkEndsWithItsToken(t *testing.T) {
	svc, _, _ := newService(t)
	ctx := context.Background()
	if _, err := svc.SaveSite(ctx, admin, "blog", nil); err != nil {
		t.Fatal(err)
	}
	tok, id := tokenActor(t, svc, "ci")
	link, err := svc.CreateUploadLink(ctx, tok, "blog")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteToken(ctx, admin, id); err != nil {
		t.Fatal(err)
	}
	_, err = svc.PublishWithUploadLink(ctx, link.ID, tarGz(t, map[string]string{"index.html": "x"}))
	wantKind(t, err, NotFound)
}
