package reader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	anubisChallengeElement  = "anubis_challenge"
	anubisBasePrefixElement = "anubis_base_prefix"
	anubisPassPath          = "/.within.website/x/cmd/anubis/api/pass-challenge"
	// Difficulty is measured in leading hexadecimal zeroes. Values above six
	// can require hundreds of millions of hashes and let a remote page consume
	// unreasonable CPU through web_read.
	maxAnubisDifficulty = 6
)

type anubisPageData struct {
	Rules struct {
		Algorithm  string `json:"algorithm"`
		Difficulty int    `json:"difficulty"`
	} `json:"rules"`
	Challenge struct {
		ID         string `json:"id"`
		RandomData string `json:"randomData"`
	} `json:"challenge"`
	BasePrefix string
}

// parseAnubisChallenge recognizes the JSON data embedded in Anubis challenge
// pages. Merely mentioning Anubis in ordinary page text is not enough.
func parseAnubisChallenge(body []byte) (anubisPageData, bool, error) {
	doc, err := goquery.NewDocumentFromReader(strings.NewReader(string(body)))
	if err != nil {
		return anubisPageData{}, true, fmt.Errorf("parse Anubis challenge HTML: %w", err)
	}
	challengeJSON := strings.TrimSpace(doc.Find("#" + anubisChallengeElement).First().Text())
	if challengeJSON == "" {
		return anubisPageData{}, false, nil
	}

	var result anubisPageData
	if err := json.Unmarshal([]byte(challengeJSON), &result); err != nil {
		return result, true, fmt.Errorf("parse Anubis challenge: %w", err)
	}
	baseJSON := strings.TrimSpace(doc.Find("#" + anubisBasePrefixElement).First().Text())
	if baseJSON != "" {
		if err := json.Unmarshal([]byte(baseJSON), &result.BasePrefix); err != nil {
			return result, true, fmt.Errorf("parse Anubis base prefix: %w", err)
		}
	}
	if result.Challenge.ID == "" || result.Challenge.RandomData == "" || result.Rules.Difficulty <= 0 {
		return result, true, fmt.Errorf("anubis challenge is missing required fields")
	}
	if result.Rules.Algorithm != "fast" && result.Rules.Algorithm != "slow" {
		return result, true, fmt.Errorf("unsupported Anubis algorithm %q", result.Rules.Algorithm)
	}
	if result.Rules.Difficulty > maxAnubisDifficulty {
		return result, true, fmt.Errorf("anubis difficulty %d exceeds safety limit %d", result.Rules.Difficulty, maxAnubisDifficulty)
	}
	return result, true, nil
}

// solveAnubisPoW finds nonce such that SHA256(randomData + decimal(nonce)) has
// difficulty leading hexadecimal zeroes, matching Anubis's browser worker.
func solveAnubisPoW(ctx context.Context, randomData string, difficulty int) (string, uint64, error) {
	workers := runtime.GOMAXPROCS(0)
	if workers < 1 {
		workers = 1
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	type solution struct {
		hash  string
		nonce uint64
	}
	found := make(chan solution, 1)
	var wg sync.WaitGroup
	var stopped atomic.Bool
	prefix := strings.Repeat("0", difficulty)

	var workerTotal uint64
	for worker := 0; worker < workers; worker++ {
		workerTotal++
	}
	for worker := uint64(0); worker < workerTotal; worker++ {
		wg.Add(1)
		go func(start uint64) {
			defer wg.Done()
			step := workerTotal
			for nonce := start; ; nonce += step {
				if stopped.Load() || workCtx.Err() != nil {
					return
				}
				sum := sha256.Sum256([]byte(randomData + strconv.FormatUint(nonce, 10)))
				encoded := hex.EncodeToString(sum[:])
				if strings.HasPrefix(encoded, prefix) {
					if stopped.CompareAndSwap(false, true) {
						found <- solution{hash: encoded, nonce: nonce}
					}
					return
				}
			}
		}(worker)
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case result := <-found:
		cancel()
		return result.hash, result.nonce, nil
	case <-done:
		if err := workCtx.Err(); err != nil {
			return "", 0, err
		}
		return "", 0, fmt.Errorf("anubis proof-of-work stopped without a solution")
	case <-ctx.Done():
		return "", 0, ctx.Err()
	}
}

// passAnubisChallenge solves and submits a detected challenge. The client's
// cookie jar retains the signed Anubis token while redirects lead back to the
// originally requested page.
func passAnubisChallenge(ctx context.Context, client *http.Client, originalURL string, challenge anubisPageData) (*http.Response, error) {
	started := time.Now()
	hash, nonce, err := solveAnubisPoW(ctx, challenge.Challenge.RandomData, challenge.Rules.Difficulty)
	if err != nil {
		return nil, fmt.Errorf("solve Anubis proof-of-work: %w", err)
	}

	original, err := url.Parse(originalURL)
	if err != nil {
		return nil, err
	}
	passURL := *original
	passURL.Path = strings.TrimRight(challenge.BasePrefix, "/") + anubisPassPath
	passURL.RawPath = ""
	query := url.Values{}
	query.Set("id", challenge.Challenge.ID)
	query.Set("response", hash)
	query.Set("nonce", strconv.FormatUint(nonce, 10))
	query.Set("redir", originalURL)
	query.Set("elapsedTime", strconv.FormatInt(time.Since(started).Milliseconds(), 10))
	passURL.RawQuery = query.Encode()
	passURL.Fragment = ""

	req, err := newRequest(ctx, passURL.String(), defaultAccept)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("submit Anubis proof-of-work: %w", err)
	}
	return resp, nil
}
