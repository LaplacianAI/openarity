package slack_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway"
	"github.com/LaplacianAI/openarity/apps/brain/internal/gateway/slack"
)

func setup(t *testing.T) gateway.Setup {
	t.Helper()
	s, ok := slack.New().(gateway.Setup)
	if !ok {
		t.Fatal("slack is not a Setup")
	}
	return s
}

// A token not shaped like a bot's is refused before it goes anywhere: a user
// token would act as the person who made it.
func TestOnlyABotTokenIsAskedAbout(t *testing.T) {
	for _, bad := range []string{"", "xoxp-1-2-3", "xapp-1-A0-1", "xoxb-", "xoxb-1 2"} {
		if _, err := setup(t).SetupRequest(gateway.Credentials{slack.KeyBotToken: bad}); err == nil {
			t.Errorf("token %q was sent", bad)
		}
	}
	r, err := setup(t).SetupRequest(gateway.Credentials{slack.KeyBotToken: token})
	if err != nil {
		t.Fatalf("SetupRequest: %v", err)
	}
	if r.URL != "https://slack.com/api/auth.test" || r.Header.Get("Authorization") != "Bearer "+token {
		t.Errorf("request %+v", r)
	}
}

func TestAuthTestGivesTheBotsUserID(t *testing.T) {
	got, warnings, err := setup(t).Complete(http.StatusOK,
		[]byte(`{"ok":true,"url":"https://acme.slack.com/","team":"Acme","user":"openarity","team_id":"T061EG9R6","user_id":"U0BOT1234","bot_id":"B0BOT"}`))
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Complete: %v %v", err, warnings)
	}
	if got[slack.KeyBotUserID] != "U0BOT1234" || len(got) != 1 {
		t.Errorf("credentials %v", got)
	}
}

// A refusal says what is wrong and never echoes the token.
func TestARefusedTokenSaysWhy(t *testing.T) {
	cases := map[string]string{
		`{"ok":false,"error":"invalid_auth"}`:            "not one Slack knows",
		`{"ok":false,"error":"token_revoked"}`:           "not one Slack knows",
		`{"ok":false,"error":"team_access_not_granted"}`: "team_access_not_granted",
		`{"ok":true,"user_id":"U0PERSON1"}`:              "not a bot",
		`{"ok":true,"user_id":"nope","bot_id":"B0BOT"}`:  "not a bot",
		`not json`: "could not be read",
	}
	for answer, want := range cases {
		_, _, err := setup(t).Complete(http.StatusOK, []byte(answer))
		if err == nil || !strings.Contains(err.Error(), want) || strings.Contains(err.Error(), token) {
			t.Errorf("Complete(%s) = %v, want %q", answer, err, want)
		}
	}
	if _, _, err := setup(t).Complete(http.StatusInternalServerError, nil); err == nil {
		t.Error("a 500 was taken")
	}
}

func downloader(t *testing.T) gateway.Downloader {
	t.Helper()
	d, ok := slack.New().(gateway.Downloader)
	if !ok {
		t.Fatal("slack is not a Downloader")
	}
	return d
}

// A file is one GET, with the token in a header, and only from Slack's file
// host: the token goes wherever the ref says.
func TestAFileIsDownloadedOnlyFromSlack(t *testing.T) {
	ref := "https://files.slack.com/files-pri/T061EG9R6-F0S43PZDF/download/report.pdf"
	r, final, err := downloader(t).DownloadRequest(ref, nil, creds())
	if err != nil || !final {
		t.Fatalf("DownloadRequest: %v final %v", err, final)
	}
	if r.Method != http.MethodGet || r.URL != ref || r.Header.Get("Authorization") != "Bearer "+token {
		t.Errorf("request %+v", r)
	}
	for _, bad := range []string{
		"http://files.slack.com/x",
		"https://files.slack.com.evil.example/x",
		"https://evil.example/files.slack.com/x",
		"https://user@files.slack.com/x",
		"files.slack.com/x",
		"::",
	} {
		if _, _, err := downloader(t).DownloadRequest(bad, nil, creds()); err == nil {
			t.Errorf("ref %q was asked for with the token", bad)
		}
	}
	if _, _, err := downloader(t).DownloadRequest(ref, nil, gateway.Credentials{}); err == nil {
		t.Error("a download with no token was built")
	}
}

// Slack answers a file the token may not read with its sign-in page, as a 200.
func TestASignInPageIsNotAFile(t *testing.T) {
	r, _, err := downloader(t).DownloadRequest("https://files.slack.com/files-pri/T-F/download/a.pdf", nil, creds())
	if err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"<!DOCTYPE html><html>", "  \n<html lang=en>"} {
		if err := r.Accepted(http.StatusOK, []byte(page)); err == nil {
			t.Errorf("%q was taken as the file", page)
		}
	}
	for _, file := range []string{"%PDF-1.7", "", "plain words"} {
		if err := r.Accepted(http.StatusOK, []byte(file)); err != nil {
			t.Errorf("%q refused: %v", file, err)
		}
	}
}

func TestSlackIssuesItsOwnSigningSecret(t *testing.T) {
	reg, err := gateway.NewRegistry(slack.New())
	if err != nil {
		t.Fatal(err)
	}
	if !reg.IssuesSigningSecret("slack") || !reg.RepliesWithEvents("slack") || reg.TakesReplyURL("slack") {
		t.Error("slack's capabilities are not what the registry says")
	}
	if _, ok := reg.Connector("slack"); ok {
		t.Error("slack has no API to point it at a channel")
	}
}
