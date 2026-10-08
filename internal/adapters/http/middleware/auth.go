package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"backend-challenge-go/internal/platform/logging"
	"backend-challenge-go/internal/ports"
)

const (
	RoleInternalService = "internal-service"
	RoleProvider        = "provider"
)

var ErrCrossProviderAccess = errors.New("middleware: token identity does not match the requested providerId")

var ErrInternalServiceOnly = errors.New("middleware: operation restricted to the internal service identity")

type claimsCtxKey struct{}

func ClaimsFromContext(ctx context.Context) (ports.Claims, bool) {
	c, ok := ctx.Value(claimsCtxKey{}).(ports.Claims)
	return c, ok
}

func intoContext(ctx context.Context, c ports.Claims) context.Context {
	return context.WithValue(ctx, claimsCtxKey{}, c)
}

func Authenticate(validator ports.TokenValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, err := bearerToken(r)
			if err != nil {
				writeUnauthorized(w)
				return
			}

			claims, err := validator.ValidateToken(r.Context(), raw)
			if err != nil {
				writeUnauthorized(w)
				return
			}

			ctx := intoContext(r.Context(), claims)
			logger := logging.FromContext(ctx)
			if claims.ProviderID != "" {
				logger = logging.WithProviderID(logger, claims.ProviderID)
			}
			ctx = logging.IntoContext(ctx, logger)

			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func bearerToken(r *http.Request) (string, error) {
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, prefix) {
		return "", ports.ErrInvalidToken
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return "", ports.ErrInvalidToken
	}
	return token, nil
}

func RequireInternalService(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims, ok := ClaimsFromContext(r.Context())
		if !ok || !claims.HasRole(RoleInternalService) {
			writeForbidden(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func AuthorizeProvider(claims ports.Claims, providerID string) error {
	if claims.HasRole(RoleInternalService) {
		return nil
	}
	if providerID == "" || claims.ProviderID != providerID {
		return ErrCrossProviderAccess
	}
	return nil
}

func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeAuthError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing or invalid bearer token")
}

func writeForbidden(w http.ResponseWriter) {
	writeAuthError(w, http.StatusForbidden, "FORBIDDEN", "operation restricted to the internal service identity")
}

func writeAuthError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`{"code":"` + code + `","message":"` + message + `"}`))
}
