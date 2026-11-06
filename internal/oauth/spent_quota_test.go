package oauth

import "testing"

func TestSpentQuotaReasonsAreOneSetForEveryCaller(t *testing.T) {
	for _, r := range []string{"quotaExceeded", "dailyLimitExceeded", "dailyLimitExceededUnreg"} {
		if !IsSpentQuotaReason(r) || !bodyNamesSpentQuota([]byte(`{"error":{"errors":[{"reason":"`+r+`"}]}}`)) {
			t.Errorf("%q is not read as a spent quota", r)
		}
	}
	for _, r := range []string{"", "rateLimitExceeded", "userRateLimitExceeded", "forbidden"} {
		if IsSpentQuotaReason(r) || bodyNamesSpentQuota([]byte(`{"reason":"`+r+`"}`)) {
			t.Errorf("%q is a short window or a permission, not a spent quota", r)
		}
	}
}
