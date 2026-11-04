package webui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct{ calls int }

func (b *fakeBackend) List(context.Context) ([]Task, error) {
	return []Task{{ID: "one", Title: "Example", Status: "idle"}}, nil
}
func (b *fakeBackend) Get(context.Context, string) (Task, error) { return Task{ID: "one"}, nil }
func (b *fakeBackend) Create(context.Context, string) (Task, error) {
	b.calls++
	return Task{ID: "one"}, nil
}
func (b *fakeBackend) Start(context.Context, string, string) error      { b.calls++; return nil }
func (b *fakeBackend) Cancel(context.Context, string) error             { b.calls++; return nil }
func (b *fakeBackend) Act(context.Context, string, ActionRequest) error { b.calls++; return nil }

const token = "0123456789abcdef0123456789abcdef"

func testServer(t *testing.T) (http.Handler, *fakeBackend) {
	t.Helper()
	b := &fakeBackend{}
	h, err := New(Config{Origin: "http://127.0.0.1:8080", Token: token, Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	return h, b
}
func request(h http.Handler, method, path, body string, auth bool, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://127.0.0.1:8080"+path, strings.NewReader(body))
	if auth {
		r.SetBasicAuth("qi", token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Qi-Request", "1")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}
func TestEveryRouteRequiresAuthentication(t *testing.T) {
	h, _ := testServer(t)
	for _, path := range []string{"/", "/app.js", "/style.css", "/api/tasks", "/api/tasks/one", "/not-found"} {
		w := request(h, "GET", path, "", false, "")
		if w.Code != 401 {
			t.Errorf("%s: %d", path, w.Code)
		}
	}
}
func TestSecurityHeadersAndAssets(t *testing.T) {
	h, _ := testServer(t)
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		w := request(h, "GET", path, "", true, "")
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		for _, key := range []string{"Content-Security-Policy", "X-Content-Type-Options", "Referrer-Policy", "X-Frame-Options", "Cache-Control"} {
			if w.Header().Get(key) == "" {
				t.Error(key)
			}
		}
		if strings.Contains(w.Body.String(), token) {
			t.Fatal("credential leaked")
		}
	}
}
func TestOriginAndHostProtection(t *testing.T) {
	h, b := testServer(t)
	for _, origin := range []string{"", "https://evil.example", "null", "http://127.0.0.1:8080.evil.example"} {
		w := request(h, "POST", "/api/tasks", `{"title":"hello"}`, true, origin)
		if w.Code != 403 {
			t.Errorf("origin %q: %d", origin, w.Code)
		}
	}
	r := httptest.NewRequest("GET", "http://evil.example/api/tasks", nil)
	r.SetBasicAuth("qi", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if b.calls != 0 {
		t.Fatal("backend called")
	}
}
func TestStrictBoundedJSON(t *testing.T) {
	h, b := testServer(t)
	for _, body := range []string{`{"title":"ok","extra":true}`, `{"title":"ok"}{}`, `null`, `{"title":""}`, `{"title":"` + strings.Repeat("x", 40000) + `"}`} {
		w := request(h, "POST", "/api/tasks", body, true, "http://127.0.0.1:8080")
		if w.Code != 400 {
			t.Errorf("expected 400 got %d", w.Code)
		}
	}
	if b.calls != 0 {
		t.Fatal("backend called")
	}
	w := request(h, "POST", "/api/tasks", `{"title":"ok"}`, true, "http://127.0.0.1:8080")
	if w.Code != 202 || b.calls != 1 {
		t.Fatal(w.Code, b.calls)
	}
}
func TestMutationNeedsCustomHeader(t *testing.T) {
	h, _ := testServer(t)
	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/tasks", strings.NewReader(`{"title":"ok"}`))
	r.SetBasicAuth("qi", token)
	r.Header.Set("Origin", "http://127.0.0.1:8080")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestAuthenticationRateLimit(t *testing.T) {
	h, _ := testServer(t)
	for i := 0; i < 21; i++ {
		w := request(h, "GET", "/", "", false, "")
		if i == 20 && w.Code != 429 {
			t.Fatal(w.Code)
		}
	}
	if w := request(h, "GET", "/", "", true, ""); w.Code != 429 {
		t.Fatal("blocked peer must not test even a correct credential")
	}
}
func TestConfiguration(t *testing.T) {
	for _, origin := range []string{"http://example.com", "http://localhost:8080", "https://example.com/", "https://user@example.com", "https://example.com?q=x", "https://example.com/#x"} {
		if _, err := New(Config{Origin: origin, Token: token, Backend: &fakeBackend{}}); err == nil {
			t.Errorf("accepted %s", origin)
		}
	}
	if _, err := New(Config{Origin: "https://example.com", Token: token, Backend: &fakeBackend{}}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskMutations(t *testing.T) {
	h, b := testServer(t)
	for _, tc := range []struct {
		path, body string
		want       int
	}{
		{"/api/tasks/one/messages", `{"prompt":"hello"}`, 200},
		{"/api/tasks/one/cancel", `{}`, 200},
		{"/api/tasks/one/actions", `{"card_id":"c","action":"approve","revision":1}`, 200},
		{"/api/tasks/one/actions", `{"card_id":"c","action":"approve","revision":0}`, 400},
		{"/api/tasks/one/cancel", `null`, 400},
		{"/api/tasks/one/messages", `{"prompt":""}`, 400},
	} {
		w := request(h, "POST", tc.path, tc.body, true, "http://127.0.0.1:8080")
		if w.Code != tc.want {
			t.Errorf("%s: %d %s", tc.path, w.Code, w.Body.String())
		}
	}
	if b.calls != 3 {
		t.Fatal(b.calls)
	}
}
func TestFetchMetadataAndReadOrigin(t *testing.T) {
	h, _ := testServer(t)
	r := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/tasks", nil)
	r.SetBasicAuth("qi", token)
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w := request(h, "GET", "/api/tasks", "", true, "https://evil.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
}

func TestLoginLimiterPeerIsolationAndExpiry(t *testing.T) {
	h, _ := testServer(t)
	s := h.(*server)
	now := time.Now()
	s.now = func() time.Time { return now }
	send := func(peer, forwarded, password string) int {
		r := httptest.NewRequest("GET", "http://127.0.0.1:8080/", nil)
		r.RemoteAddr = peer
		r.SetBasicAuth("qi", password)
		r.Header.Set("X-Forwarded-For", forwarded)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	for i := 0; i < 20; i++ {
		if got := send("192.0.2.1:1000", "198.51.100.1", "wrong"); got != 401 {
			t.Fatal(got)
		}
	}
	if got := send("192.0.2.1:2000", "198.51.100.2", token); got != 429 {
		t.Fatal("port or forwarded header bypass", got)
	}
	if got := send("192.0.2.2:1000", "198.51.100.1", token); got != 200 {
		t.Fatal("unrelated peer blocked", got)
	}
	now = now.Add(time.Minute)
	if got := send("192.0.2.1:3000", "", token); got != 200 {
		t.Fatal("expired window blocked", got)
	}
}
func TestLoginLimiterBoundedMemory(t *testing.T) {
	h, _ := testServer(t)
	s := h.(*server)
	now := time.Now()
	s.now = func() time.Time { return now }
	for i := 0; i < maxLoginPeers; i++ {
		s.attempts[string(rune(i))] = loginWindow{failures: 1, expires: now.Add(time.Minute)}
	}
	if w := request(h, "GET", "/", "", true, ""); w.Code != 429 {
		t.Fatal(w.Code)
	}
	if len(s.attempts) != maxLoginPeers {
		t.Fatal("unbounded map")
	}
	now = now.Add(time.Minute)
	if w := request(h, "GET", "/", "", true, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
}

func TestSuccessfulPollingDoesNotResetFailedGuessBudget(t *testing.T) {
	h, _ := testServer(t)
	for i := 0; i < 20; i++ {
		if w := request(h, "GET", "/", "", false, ""); w.Code != 401 {
			t.Fatal(w.Code)
		}
		if i < 19 {
			if w := request(h, "GET", "/", "", true, ""); w.Code != 200 {
				t.Fatal(w.Code)
			}
		}
	}
	if w := request(h, "GET", "/", "", true, ""); w.Code != 429 {
		t.Fatal("successful requests reset failure budget", w.Code)
	}
}
