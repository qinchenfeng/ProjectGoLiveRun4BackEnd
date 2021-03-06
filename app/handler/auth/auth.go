package auth

import (
	"net/http"
	"strings"
)

func Auth(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		return
	}
	chartType := strings.TrimSpace(r.URL.Query().Get("option"))
	if chartType == "login" {
		Login(w, r)
	} else if chartType == "token-verify" {
		TokenVerify(w, r)
	}
}
