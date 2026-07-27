package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBearerTokenExtraction(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "no header yields nothing", header: "", want: ""},
		{name: "bearer credential is returned", header: "Bearer pmk_abc123", want: "pmk_abc123"},
		{name: "scheme is case-insensitive per RFC 7235", header: "bearer pmk_abc123", want: "pmk_abc123"},
		{name: "surrounding whitespace is trimmed", header: "Bearer   pmk_abc123  ", want: "pmk_abc123"},
		{name: "basic auth is not a bearer token", header: "Basic YWRtaW46aHVudGVyMg==", want: ""},
		{name: "scheme without a credential yields nothing", header: "Bearer ", want: ""},
		{name: "bare credential without a scheme is ignored", header: "pmk_abc123", want: ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/api/v1/status", nil)
			if tc.header != "" {
				r.Header.Set("Authorization", tc.header)
			}
			if got := BearerToken(r); got != tc.want {
				t.Errorf("BearerToken = %q, want %q", got, tc.want)
			}
		})
	}
}
