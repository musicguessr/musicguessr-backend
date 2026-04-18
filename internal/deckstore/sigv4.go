package deckstore

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const awsAlgorithm = "AWS4-HMAC-SHA256"

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func hexSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func signingKey(secretKey, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secretKey), []byte(date))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// signRequest adds Authorization, x-amz-date, and x-amz-content-sha256 headers to req.
func signRequest(req *http.Request, accessKey, secretKey, region string, body []byte) {
	now := time.Now().UTC()
	date := now.Format("20060102")
	datetime := now.Format("20060102T150405Z")

	payloadHash := hexSHA256(body)
	req.Header.Set("x-amz-date", datetime)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}

	// Canonical headers — must be sorted and lowercase
	signedHeaders, canonicalHeaders := buildCanonicalHeaders(req)

	canonicalURI := req.URL.EscapedPath()
	if canonicalURI == "" {
		canonicalURI = "/"
	}

	canonicalQueryString := buildCanonicalQuery(req)

	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI,
		canonicalQueryString,
		canonicalHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	credentialScope := fmt.Sprintf("%s/%s/s3/aws4_request", date, region)
	stringToSign := strings.Join([]string{
		awsAlgorithm,
		datetime,
		credentialScope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	key := signingKey(secretKey, date, region, "s3")
	sig := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	req.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		awsAlgorithm, accessKey, credentialScope, signedHeaders, sig,
	))
}

func buildCanonicalHeaders(req *http.Request) (signedHeaders, canonicalHeaders string) {
	type kv struct{ k, v string }
	var pairs []kv
	for k, vs := range req.Header {
		lk := strings.ToLower(k)
		if lk == "host" || strings.HasPrefix(lk, "x-amz-") || lk == "content-type" {
			pairs = append(pairs, kv{lk, strings.TrimSpace(vs[0])})
		}
	}
	// host must be included
	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	pairs = append(pairs, kv{"host", host})

	// deduplicate
	seen := map[string]bool{}
	var unique []kv
	for _, p := range pairs {
		if !seen[p.k] {
			seen[p.k] = true
			unique = append(unique, p)
		}
	}

	sort.Slice(unique, func(i, j int) bool { return unique[i].k < unique[j].k })

	var hdrs, keys []string
	for _, p := range unique {
		hdrs = append(hdrs, p.k+":"+p.v)
		keys = append(keys, p.k)
	}
	return strings.Join(keys, ";"), strings.Join(hdrs, "\n") + "\n"
}

func buildCanonicalQuery(req *http.Request) string {
	q := req.URL.Query()
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		for _, v := range q[k] {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}
