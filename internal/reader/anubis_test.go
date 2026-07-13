package reader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestSolveAnubisPoW(t *testing.T) {
	hash, nonce, err := solveAnubisPoW(context.Background(), "challenge-data", 3)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("challenge-data" + strconv.FormatUint(nonce, 10)))
	if hash != hex.EncodeToString(sum[:]) || !strings.HasPrefix(hash, "000") {
		t.Fatalf("invalid solution hash=%q nonce=%d", hash, nonce)
	}
}

func TestFetchGenericSolvesAnubisChallenge(t *testing.T) {
	const randomData = "test-random-data"
	var baseURL string
	mux := http.NewServeMux()
	mux.HandleFunc("/article", func(w http.ResponseWriter, r *http.Request) {
		if cookie, _ := r.Cookie("techaro.lol-anubis-auth"); cookie != nil && cookie.Value == "passed" {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte("<html><body><main><h1>Readable article</h1><p>Challenge passed.</p></main></body></html>"))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprintf(w, `<html><body><script id="anubis_challenge" type="application/json">{"rules":{"algorithm":"fast","difficulty":2},"challenge":{"id":"test-id","randomData":%q}}</script><script id="anubis_base_prefix" type="application/json">""</script></body></html>`, randomData)
	})
	mux.HandleFunc(anubisPassPath, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("id") != "test-id" {
			t.Error("wrong challenge id")
		}
		nonce := r.URL.Query().Get("nonce")
		sum := sha256.Sum256([]byte(randomData + nonce))
		if got := r.URL.Query().Get("response"); got != hex.EncodeToString(sum[:]) || !strings.HasPrefix(got, "00") {
			t.Errorf("invalid proof response %q", got)
		}
		http.SetCookie(w, &http.Cookie{Name: "techaro.lol-anubis-auth", Value: "passed", Path: "/"})
		http.Redirect(w, r, baseURL+"/article", http.StatusFound)
	})
	server := httptest.NewServer(mux)
	defer server.Close()
	baseURL = server.URL

	previous := allowPrivateHosts
	allowPrivateHosts = true
	t.Cleanup(func() { allowPrivateHosts = previous })
	content, err := fetchGenericHTMLAsMarkdown(context.Background(), newHTTPClient(), server.URL+"/article")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "Readable article") || strings.Contains(content, "anubis_challenge") {
		t.Fatalf("unexpected content: %s", content)
	}
}

func TestParseAnubisRejectsExcessiveDifficulty(t *testing.T) {
	body := []byte(`<script id="anubis_challenge">{"rules":{"algorithm":"fast","difficulty":7},"challenge":{"id":"x","randomData":"y"}}</script>`)
	_, detected, err := parseAnubisChallenge(body)
	if !detected || err == nil || !strings.Contains(err.Error(), "safety limit") {
		t.Fatalf("detected=%v err=%v", detected, err)
	}
}
