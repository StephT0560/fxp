package metrics

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestSessionsEndpointIsOptInAndBounded(t *testing.T) {
	t.Cleanup(func() { SetPresence(nil) })
	get := func(method string) (int, string) {
		w := httptest.NewRecorder()
		sessionsHandler(w, httptest.NewRequest(method, "/sessions", nil))
		return w.Code, w.Body.String()
	}
	if code, _ := get("GET"); code != 404 {
		t.Fatalf("disabled presence must 404, got %d", code)
	}
	SetPresence(func() []TraderSessions {
		out := []TraderSessions{}
		for i := 0; i < maxPresenceTraders+10; i++ {
			out = append(out, TraderSessions{Trader: fmt.Sprintf("ws:bot-%03d", i), Sessions: 1})
		}
		return out
	})
	if code, _ := get("POST"); code != 405 {
		t.Fatalf("POST must be rejected, got %d", code)
	}
	code, body := get("GET")
	var got struct{ Traders []TraderSessions }
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got.Traders) != maxPresenceTraders || got.Traders[0].Trader != "ws:bot-000" {
		t.Fatalf("unexpected response %d %s", code, body)
	}
}
