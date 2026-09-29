package identity

import (
	"errors"
	"fmt"
	"testing"

	"firebase.google.com/go/v4/appcheck"
	"github.com/golang-jwt/jwt/v4"
)

func TestAppCheckFailureReason(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"audience", fmt.Errorf("wrapped: %w", appcheck.ErrTokenAudience), "audience"},
		{"issuer", appcheck.ErrTokenIssuer, "issuer"},
		{"algorithm", appcheck.ErrIncorrectAlgorithm, "algorithm"},
		{"expired", &jwt.ValidationError{Errors: jwt.ValidationErrorExpired}, "expired"},
		{"signature", &jwt.ValidationError{Errors: jwt.ValidationErrorSignatureInvalid}, "signature"},
		{"unverifiable", &jwt.ValidationError{Errors: jwt.ValidationErrorUnverifiable}, "unverifiable"},
		{"other", errors.New("sensitive token must not appear"), "verification_failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := appCheckFailureReason(test.err); got != test.want {
				t.Fatalf("reason = %q, want %q", got, test.want)
			}
		})
	}
}
