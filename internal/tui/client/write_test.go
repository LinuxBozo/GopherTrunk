package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestEndCall_PostsBody(t *testing.T) {
	var got struct {
		Method, Path, Body string
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Method = r.Method
		got.Path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		got.Body = string(body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)

	if err := c.EndCall(context.Background(), "00000001", "manual"); err != nil {
		t.Fatal(err)
	}
	if got.Method != "POST" {
		t.Errorf("method = %s", got.Method)
	}
	if got.Path != "/api/v1/calls/00000001/end" {
		t.Errorf("path = %s", got.Path)
	}
	if !strings.Contains(got.Body, `"reason":"manual"`) {
		t.Errorf("body = %s", got.Body)
	}
}

func TestEndCall_403_SurfacesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"mutations disabled"}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	err := c.EndCall(context.Background(), "abc", "")
	if err == nil {
		t.Fatal("want error")
	}
	var herr *HTTPError
	if !asHTTPErr(err, &herr) {
		t.Fatalf("want *HTTPError, got %T", err)
	}
	if herr.Status != 403 {
		t.Errorf("status = %d", herr.Status)
	}
}

func TestUpdateTalkgroup_OmitsNilFields(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		body = string(buf)
		_, _ = w.Write([]byte(`{"id":42,"alpha_tag":"x","priority":3}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	pri := 3
	out, err := c.UpdateTalkgroup(context.Background(), 42, &pri, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if out.ID != 42 {
		t.Errorf("ID = %d", out.ID)
	}
	if !strings.Contains(body, `"priority":3`) {
		t.Errorf("body = %s", body)
	}
	if strings.Contains(body, `"lockout"`) {
		t.Errorf("body should omit lockout when nil: %s", body)
	}
	if strings.Contains(body, `"scan"`) {
		t.Errorf("body should omit scan when nil: %s", body)
	}
}

func TestUpdateTalkgroup_PassesScanFlag(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf, _ := io.ReadAll(r.Body)
		body = string(buf)
		_, _ = w.Write([]byte(`{"id":7,"alpha_tag":"x","scan":false}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	scan := false
	out, err := c.UpdateTalkgroup(context.Background(), 7, nil, nil, &scan)
	if err != nil {
		t.Fatal(err)
	}
	if out.Scan {
		t.Errorf("out.Scan = true, want false")
	}
	if !strings.Contains(body, `"scan":false`) {
		t.Errorf("body should include scan=false: %s", body)
	}
}

func TestUpdateTalkgroup_RequiresAtLeastOneField(t *testing.T) {
	c := New("http://example.invalid", time.Second, false)
	_, err := c.UpdateTalkgroup(context.Background(), 42, nil, nil, nil)
	if err == nil {
		t.Fatal("want error when all fields are nil")
	}
}

func TestSweepRetention_PostsEmpty(t *testing.T) {
	got := struct {
		method, path string
		bodyLen      int
	}{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path = r.Method, r.URL.Path
		body, _ := io.ReadAll(r.Body)
		got.bodyLen = len(body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	if err := c.SweepRetention(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got.method != "POST" || got.path != "/api/v1/retention/sweep" {
		t.Errorf("method=%s path=%s", got.method, got.path)
	}
	if got.bodyLen != 0 {
		t.Errorf("body should be empty, got %d bytes", got.bodyLen)
	}
}

func TestResetToneDevice_PathHasSerial(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	if err := c.ResetToneDevice(context.Background(), "00000001"); err != nil {
		t.Fatal(err)
	}
	if path != "/api/v1/devices/00000001/tone-reset" {
		t.Errorf("path = %s", path)
	}
}

func TestMutationStatus_Decodes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]bool{
			"allow_mutations":    true,
			"engine_writable":    true,
			"retention_writable": false,
			"tones_writable":     true,
		})
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	s, err := c.MutationStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.AllowMutations || !s.EngineWritable || s.RetentionWritable || !s.TonesWritable {
		t.Errorf("decoded unexpectedly: %+v", s)
	}
}

func TestMutationStatus_404_ReturnsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	s, err := c.MutationStatus(context.Background())
	if err != nil {
		t.Fatalf("404 should not return error, got %v", err)
	}
	if s != (MutationStatus{}) {
		t.Errorf("want zero MutationStatus on 404, got %+v", s)
	}
}

func TestAuthHeader_SetTokenAttachesBearer(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	c.SetToken("secret-token")
	if err := c.EndCall(context.Background(), "abc", "manual"); err != nil {
		t.Fatalf("EndCall: %v", err)
	}
	if gotAuth != "Bearer secret-token" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret-token")
	}
}

func TestAuthHeader_NoTokenOmitsHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	if err := c.EndCall(context.Background(), "abc", "manual"); err != nil {
		t.Fatalf("EndCall: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty", gotAuth)
	}
}

func TestAuthHeader_TokenFileReloadedPerRequest(t *testing.T) {
	dir := t.TempDir()
	tokPath := dir + "/token"
	if err := os.WriteFile(tokPath, []byte("first-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	if err := c.SetTokenFile(tokPath); err != nil {
		t.Fatal(err)
	}
	if err := c.EndCall(context.Background(), "abc", "manual"); err != nil {
		t.Fatalf("EndCall: %v", err)
	}
	if gotAuth != "Bearer first-tok" {
		t.Errorf("first request: Authorization = %q, want %q", gotAuth, "Bearer first-tok")
	}
	// Rotate the file; the next request should pick it up automatically.
	if err := os.WriteFile(tokPath, []byte("second-tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := c.EndCall(context.Background(), "abc", "manual"); err != nil {
		t.Fatalf("EndCall: %v", err)
	}
	if gotAuth != "Bearer second-tok" {
		t.Errorf("after rotation: Authorization = %q, want %q", gotAuth, "Bearer second-tok")
	}
}

// TestTalkgroupHoldAvoid_Routes pins the four scan-control calls against
// the daemon's routes and body shapes (handlers_scanner.go).
func TestTalkgroupHoldAvoid_Routes(t *testing.T) {
	type call struct{ Method, Path, Query, Body string }
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, call{r.Method, r.URL.Path, r.URL.RawQuery, string(body)})
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := New(srv.URL, time.Second, false)
	ctx := context.Background()
	if err := c.TalkgroupHold(ctx, "metro", 101); err != nil {
		t.Fatal(err)
	}
	if err := c.ReleaseHold(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.TalkgroupAvoid(ctx, "", 101, 30); err != nil {
		t.Fatal(err)
	}
	if err := c.TalkgroupUnavoid(ctx, "metro east", 101); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 4 {
		t.Fatalf("calls = %d, want 4", len(calls))
	}
	if calls[0].Method != "POST" || calls[0].Path != "/api/v1/scanner/hold" ||
		!strings.Contains(calls[0].Body, `"talkgroup":101`) || !strings.Contains(calls[0].Body, `"system":"metro"`) {
		t.Errorf("hold = %+v", calls[0])
	}
	if calls[1].Method != "DELETE" || calls[1].Path != "/api/v1/scanner/hold" {
		t.Errorf("release = %+v", calls[1])
	}
	if calls[2].Method != "POST" || calls[2].Path != "/api/v1/talkgroups/101/avoid" ||
		!strings.Contains(calls[2].Body, `"minutes":30`) {
		t.Errorf("avoid = %+v", calls[2])
	}
	if calls[3].Method != "DELETE" || calls[3].Path != "/api/v1/talkgroups/101/avoid" ||
		calls[3].Query != "system=metro+east" {
		t.Errorf("unavoid = %+v", calls[3])
	}
}
