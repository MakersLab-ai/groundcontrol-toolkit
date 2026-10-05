package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIDsAreEscapedAsOnePathSegment(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.RequestURI)
		io.WriteString(w, `{"data":[],"meta":{"total":0}}`)
	}))
	defer srv.Close()
	c := New(srv.URL+"/api/v1", "gc_live_k", "")
	c.GetTask("../me")
	c.GetTask("a?b=1#x")
	c.UpdateField("t 1", "..", nil)
	c.SubmitGoalCheckin("g/1", "c", map[string]any{})
	c.GetJournalDay(".")
	want := []string{
		"/api/v1/tasks/..%2Fme",
		"/api/v1/tasks/a%3Fb=1%23x",
		"/api/v1/tables/t%201/fields/%2E%2E",
		"/api/v1/objectives/g%2F1/checkins/c/submit",
		"/api/v1/journal/%2E",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("got\n%s", strings.Join(got, "\n"))
	}
}

func TestTimeoutsAndCancel(t *testing.T) {
	c := New("http://x", "", "")
	if c.HTTP.Timeout != 30*time.Second || c.Upload.Timeout != 5*time.Minute {
		t.Fatalf("timeouts %v / %v", c.HTTP.Timeout, c.Upload.Timeout)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	c = New(srv.URL, "", "")
	c.Ctx = ctx
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	if _, err := c.GetMe(); err == nil || time.Since(start) > 3*time.Second {
		t.Fatalf("cancel did not abort the request: %v after %v", err, time.Since(start))
	}
}

func TestErrorsRedactKeys(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		io.WriteString(w, `{"error":{"message":"key gc_live_secret_123 is not allowed"}}`)
	}))
	defer srv.Close()
	_, err := New(srv.URL, "k", "").GetMe()
	if err == nil || err.Error() != "GROUNDCONTROL API error 403: key gc_live_<redacted> is not allowed" {
		t.Fatal(err)
	}
}
