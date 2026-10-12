package cpa

import (
	"errors"
	"net/http"
	"strings"
)

// RedirectPolicy returns the redirect policy shared by the CPA clients.
func RedirectPolicy(forwardAuthorization bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		previous := via[len(via)-1]
		if strings.EqualFold(previous.URL.Scheme, "https") && strings.EqualFold(req.URL.Scheme, "http") {
			return errors.New("refusing redirect from HTTPS to HTTP")
		}
		if req.URL.User != nil {
			return errors.New("refusing redirect URL with credentials")
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}

		req.Header.Del("Authorization")
		if forwardAuthorization {
			for _, value := range via[0].Header.Values("Authorization") {
				req.Header.Add("Authorization", value)
			}
		}
		return nil
	}
}
