package pushover

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClient_Enabled(t *testing.T) {
	if (&Client{}).Enabled() {
		t.Error("empty client should not be enabled")
	}
	if (&Client{AppToken: "t"}).Enabled() {
		t.Error("missing user key should not be enabled")
	}
	if !New("t", "u").Enabled() {
		t.Error("filled client should be enabled")
	}
}

func TestClient_Send(t *testing.T) {
	var got struct {
		token, user, message, title, priority string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		got.token = r.PostForm.Get("token")
		got.user = r.PostForm.Get("user")
		got.message = r.PostForm.Get("message")
		got.title = r.PostForm.Get("title")
		got.priority = r.PostForm.Get("priority")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"status":1,"request":"x"}`))
	}))
	defer srv.Close()

	c := New("the-token", "the-user")
	c.Endpoint = srv.URL
	err := c.Send(context.Background(), Message{Title: "T", Body: "hello", Priority: 1})
	if err != nil {
		t.Fatal(err)
	}
	if got.token != "the-token" || got.user != "the-user" {
		t.Errorf("creds: %+v", got)
	}
	if got.message != "hello" {
		t.Errorf("message: %q", got.message)
	}
	if got.title != "T" {
		t.Errorf("title: %q", got.title)
	}
	if got.priority != "1" {
		t.Errorf("priority: %q", got.priority)
	}
}

func TestClient_SendDisabledIsNoOp(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(200)
	}))
	defer srv.Close()
	c := &Client{Endpoint: srv.URL} // no creds
	if err := c.Send(context.Background(), Message{Body: "x"}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Errorf("expected zero requests; got %d", calls)
	}
}

func TestClient_SendServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()
	c := New("t", "u")
	c.Endpoint = srv.URL
	err := c.Send(context.Background(), Message{Body: "x"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "400") {
		t.Errorf("expected 400 in error, got: %v", err)
	}
}

func TestClient_SendEmptyBody(t *testing.T) {
	c := New("t", "u")
	if err := c.Send(context.Background(), Message{}); err == nil {
		t.Fatal("expected empty body error")
	}
}
