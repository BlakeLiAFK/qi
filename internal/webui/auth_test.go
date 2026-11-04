package webui

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func previewServer(t *testing.T) *server {
	t.Helper()
	h, err := New(Config{Origin: "https://qi.example", Token: token, Backend: &fakeBackend{}, PublicPreview: true})
	if err != nil {
		t.Fatal(err)
	}
	return h.(*server)
}
func previewRequest(s *server, method, path, body string, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "https://qi.example"+path, strings.NewReader(body))
	r.Header.Set("Origin", "https://qi.example")
	r.Header.Set("X-Qi-Request", "1")
	r.Header.Set("Content-Type", "application/json")
	if cookie != nil {
		r.AddCookie(cookie)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func loginPreview(t *testing.T, s *server) *http.Cookie {
	t.Helper()
	w := previewRequest(s, "POST", "/api/login", `{"token":"`+token+`"}`, nil)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	return w.Result().Cookies()[0]
}
func TestPreviewAuthenticationAndCookie(t *testing.T) {
	s := previewServer(t)
	for _, path := range []string{"/api/tasks", "/api/skills", "/api/auth", "/api/tasks/one/events", "/api/tasks/one/artifacts/any", "/app.js", "/unknown"} {
		w := previewRequest(s, "GET", path, "", nil)
		if w.Code != 401 {
			t.Errorf("%s: %d", path, w.Code)
		}
		if w.Header().Get("WWW-Authenticate") != "" {
			t.Fatal("Basic prompt in preview")
		}
	}
	r := httptest.NewRequest("GET", "https://qi.example/api/tasks", nil)
	r.SetBasicAuth("qi", token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("Basic bypass", w.Code)
	}
	cookie := loginPreview(t, s)
	if !cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.Path != "/" || cookie.Domain != "" || cookie.MaxAge != 28800 || len(cookie.Value) != 43 {
		t.Fatalf("unsafe cookie: %+v", cookie)
	}
	if s.cfg.Token != "" {
		t.Fatal("plaintext token retained")
	}
	if w := previewRequest(s, "GET", "/api/tasks", "", cookie); w.Code != 200 {
		t.Fatal(w.Code)
	}
}
func TestPreviewLogoutRevocationAndExpiry(t *testing.T) {
	s := previewServer(t)
	a := loginPreview(t, s)
	b := loginPreview(t, s)
	session := s.sessions[sha256.Sum256([]byte(a.Value))]
	if w := previewRequest(s, "POST", "/api/logout", `{"all":false}`, a); w.Code != 200 || w.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal(w.Code)
	}
	select {
	case <-session.ctx.Done():
	default:
		t.Fatal("logout did not revoke stream context")
	}
	if previewRequest(s, "GET", "/api/tasks", "", a).Code != 401 || previewRequest(s, "GET", "/api/tasks", "", b).Code != 200 {
		t.Fatal("incorrect targeted revocation")
	}
	c := loginPreview(t, s)
	if previewRequest(s, "POST", "/api/logout", `{"all":true}`, b).Code != 200 {
		t.Fatal("all logout failed")
	}
	if previewRequest(s, "GET", "/api/tasks", "", c).Code != 401 {
		t.Fatal("all revoke failed")
	}
	d := loginPreview(t, s)
	s.now = func() time.Time { return time.Now().Add(sessionLifetime) }
	if previewRequest(s, "GET", "/api/tasks", "", d).Code != 401 {
		t.Fatal("expiry failed")
	}
}
func TestPreviewLoginCSRFAndProxyBoundary(t *testing.T) {
	s := previewServer(t)
	for _, tc := range []struct {
		origin, header, peer, proto string
		tls                         bool
		want                        int
	}{
		{"", "1", "127.0.0.1:123", "https", false, 403},
		{"https://evil.example", "1", "127.0.0.1:123", "https", false, 403},
		{"https://qi.example", "", "127.0.0.1:123", "https", false, 403},
		{"https://qi.example", "1", "192.0.2.1:123", "https", false, 403},
		{"https://qi.example", "1", "127.0.0.1:123", "", false, 403},
		{"https://qi.example", "1", "127.0.0.1:123", "http", false, 403},
		{"https://qi.example", "1", "127.0.0.1:123", "https", false, 200},
		{"https://qi.example", "1", "192.0.2.1:123", "", true, 200},
	} {
		r := httptest.NewRequest("POST", "https://qi.example/api/login", strings.NewReader(`{"token":"`+token+`"}`))
		r.TLS = nil
		if tc.tls {
			r.TLS = &tls.ConnectionState{}
		}
		r.RemoteAddr = tc.peer
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Qi-Request", tc.header)
		r.Header.Set("X-Forwarded-Proto", tc.proto)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%+v: %d", tc, w.Code)
		}
	}
	if _, err := New(Config{Origin: "http://127.0.0.1:80", Token: token, Backend: &fakeBackend{}, PublicPreview: true}); err == nil {
		t.Fatal("HTTP preview accepted")
	}
}
func TestPreviewLimiterDoesNotBlockExistingSession(t *testing.T) {
	s := previewServer(t)
	cookie := loginPreview(t, s)
	for i := 0; i < 20; i++ {
		if previewRequest(s, "POST", "/api/login", `{"token":"wrong"}`, nil).Code != 401 {
			t.Fatal(i)
		}
	}
	if previewRequest(s, "POST", "/api/login", `{"token":"`+token+`"}`, nil).Code != 429 {
		t.Fatal("unlimited guesses")
	}
	if previewRequest(s, "GET", "/api/tasks", "", cookie).Code != 200 {
		t.Fatal("proxy lockout blocks existing session")
	}
}
func TestPreviewSessionCapacityRotationAndRestart(t *testing.T) {
	s := previewServer(t)
	cookie := loginPreview(t, s)
	rotated := previewRequest(s, "POST", "/api/login", `{"token":"`+token+`"}`, cookie)
	if rotated.Code != 200 || len(s.sessions) != 1 {
		t.Fatal("rotation failed")
	}
	if previewRequest(s, "GET", "/api/tasks", "", cookie).Code != 401 {
		t.Fatal("old session survived rotation")
	}
	if previewRequest(previewServer(t), "GET", "/api/tasks", "", rotated.Result().Cookies()[0]).Code != 401 {
		t.Fatal("session survived restart")
	}
	for i := len(s.sessions); i < maxSessions; i++ {
		key := sha256.Sum256([]byte(strings.Repeat("x", i)))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.sessions[key] = &browserSession{expires: time.Now().Add(time.Hour), ctx: ctx, cancel: cancel}
	}
	if previewRequest(s, "POST", "/api/login", `{"token":"`+token+`"}`, nil).Code != 503 || len(s.sessions) != maxSessions {
		t.Fatal("session map unbounded")
	}
}

func TestPreviewCookieMutationCSRF(t *testing.T) {
	s := previewServer(t)
	cookie := loginPreview(t, s)
	for _, path := range []string{"/api/tasks", "/api/logout"} {
		r := httptest.NewRequest("POST", "https://qi.example"+path, strings.NewReader(`{}`))
		r.AddCookie(cookie)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatal(path, w.Code)
		}
	}
	if previewRequest(s, "GET", "/api/tasks", "", cookie).Code != 200 {
		t.Fatal("CSRF logged out victim")
	}
}

func TestPreviewLogoutClosesEventStream(t *testing.T) {
	s := previewServer(t)
	s.cfg.Backend = &eventFake{}
	cookie := loginPreview(t, s)
	ts := httptest.NewTLSServer(s)
	defer ts.Close()
	req, _ := http.NewRequest("GET", ts.URL+"/api/tasks/one/events", nil)
	req.Host = "qi.example"
	req.AddCookie(cookie)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if previewRequest(s, "POST", "/api/logout", `{"all":false}`, cookie).Code != 200 {
		t.Fatal("logout failed")
	}
	done := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, resp.Body); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("revoked stream stayed open")
	}
}

// Cancel synchronously after the first replay event to prove the next page item
// is not written even when it is already fetched and requires no backend call.
type revokeOnEventWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *revokeOnEventWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseRecorder.Write(p)
	if strings.Contains(string(p), "event: progress") {
		w.cancel()
	}
	return n, err
}
func TestRevocationStopsBufferedEventReplay(t *testing.T) {
	s := previewServer(t)
	s.cfg.Backend = &eventFake{}
	cookie := loginPreview(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest("GET", "https://qi.example/api/tasks/one/events", nil).WithContext(ctx)
	r.AddCookie(cookie)
	w := &revokeOnEventWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	s.ServeHTTP(w, r)
	if got := strings.Count(w.Body.String(), "event: progress"); got != 1 {
		t.Fatalf("wrote %d replay events after synchronous cancellation", got)
	}
}
