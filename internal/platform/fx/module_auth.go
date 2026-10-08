package fxplatform

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/fx"

	authmw "backend-challenge-go/internal/adapters/http/middleware"
	"backend-challenge-go/internal/config"
	"backend-challenge-go/internal/ports"
)

var AuthModule = fx.Module("auth",
	fx.Provide(provideTokenValidator),
)

type oidcTokenValidator struct {
	cfg      config.OIDCConfig
	verifier *oidc.IDTokenVerifier
}

func (v *oidcTokenValidator) ValidateToken(ctx context.Context, rawToken string) (ports.Claims, error) {
	if v.verifier == nil {
		// Only reachable if a request arrives before this module's OnStart hook has completed OIDC
		// discovery, which should never happen (fx.Lifecycle runs every OnStart before the HTTP
		// server's own OnStart starts accepting connections) — reported as an invalid token rather
		// than panicking, since an auth failure is always the safe default.
		return ports.Claims{}, fmt.Errorf("%w: token validator not ready", ports.ErrInvalidToken)
	}

	idToken, err := v.verifier.Verify(ctx, rawToken)
	if err != nil {
		return ports.Claims{}, fmt.Errorf("%w: %v", ports.ErrInvalidToken, err)
	}

	var raw map[string]interface{}
	if err := idToken.Claims(&raw); err != nil {
		return ports.Claims{}, fmt.Errorf("%w: %v", ports.ErrInvalidToken, err)
	}

	return claimsFromRaw(raw, v.cfg), nil
}

func provideTokenValidator(lc fx.Lifecycle, cfg config.OIDCConfig, logger *slog.Logger) ports.TokenValidator {
	v := &oidcTokenValidator{cfg: cfg}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			provider, err := oidc.NewProvider(ctx, cfg.IssuerURL)
			if err != nil {
				return fmt.Errorf("auth: oidc discovery against %s: %w", cfg.IssuerURL, err)
			}

			verifierCfg := &oidc.Config{SkipClientIDCheck: true}
			if cfg.Audience != "" {
				verifierCfg = &oidc.Config{ClientID: cfg.Audience}
			}
			v.verifier = provider.Verifier(verifierCfg)

			logger.Info("auth: oidc discovery complete", slog.String("issuer", cfg.IssuerURL))
			return nil
		},
	})

	return v
}

func claimsFromRaw(raw map[string]interface{}, cfg config.OIDCConfig) ports.Claims {
	subject := stringClaim(raw, "sub")

	clientID := stringClaim(raw, "azp")
	if clientID == "" {
		clientID = stringClaim(raw, "client_id")
	}

	roles := realmRoles(raw)
	isInternal := containsRole(roles, authmw.RoleInternalService) || (clientID != "" && clientID == cfg.ClientID)
	isProvider := containsRole(roles, authmw.RoleProvider) || (!isInternal && clientID != "")

	var finalRoles []string
	providerID := stringClaim(raw, "provider_id")

	if isInternal {
		finalRoles = append(finalRoles, authmw.RoleInternalService)
	}
	if isProvider {
		finalRoles = append(finalRoles, authmw.RoleProvider)
		if providerID == "" {
			providerID = clientID
		}
	}

	return ports.Claims{Subject: subject, ProviderID: providerID, Roles: finalRoles}
}

func realmRoles(raw map[string]interface{}) []string {
	realmAccess, ok := raw["realm_access"].(map[string]interface{})
	if !ok {
		return nil
	}
	rawRoles, ok := realmAccess["roles"].([]interface{})
	if !ok {
		return nil
	}
	roles := make([]string, 0, len(rawRoles))
	for _, r := range rawRoles {
		if s, ok := r.(string); ok {
			roles = append(roles, s)
		}
	}
	return roles
}

func containsRole(roles []string, want string) bool {
	for _, r := range roles {
		if r == want {
			return true
		}
	}
	return false
}

func stringClaim(raw map[string]interface{}, key string) string {
	s, _ := raw[key].(string)
	return s
}
