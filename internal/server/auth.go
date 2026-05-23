package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/s12ryt/docker-ai-proxy/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const sessionCookieName = "ai_hub_session"

type contextKey string

const userContextKey contextKey = "ai_hub_user"

type sessionClaims struct {
	Subject  int64  `json:"sub"`
	Username string `json:"username"`
	Role     string `json:"role"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

type publicUser struct {
	ID          int64   `json:"id"`
	Username    string  `json:"username"`
	Role        string  `json:"role"`
	Disabled    bool    `json:"disabled"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	LastLoginAt *string `json:"last_login_at,omitempty"`
}

func userToPublic(u store.User) publicUser {
	out := publicUser{
		ID:        u.ID,
		Username:  u.Username,
		Role:      u.Role,
		Disabled:  u.Disabled,
		CreatedAt: u.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: u.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if u.LastLoginAt != nil {
		v := u.LastLoginAt.UTC().Format(time.RFC3339)
		out.LastLoginAt = &v
	}
	return out
}

func userFromContext(ctx context.Context) (store.User, bool) {
	u, ok := ctx.Value(userContextKey).(store.User)
	return u, ok
}

func (s *Server) handleBootstrapStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	count, err := s.store.CountUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"bootstrap_required": count == 0})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	u, err := s.store.FindUserByUsername(r.Context(), req.Username)
	if err != nil || u.Disabled || bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(req.Password)) != nil {
		http.Error(w, "invalid username or password", http.StatusUnauthorized)
		return
	}
	if err := s.setSessionCookie(w, u); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.store.UpdateUserLastLogin(r.Context(), u.ID)
	writeJSON(w, map[string]any{"ok": true, "user": userToPublic(u)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.clearSessionCookie(w)
	writeJSON(w, map[string]any{"ok": true})
}

func (s *Server) handleProfile(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	u, ok := userFromContext(r.Context())
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	count, _ := s.store.CountUsers(r.Context())
	writeJSON(w, map[string]any{"user": userToPublic(u), "bootstrap_required": count == 0})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if len(req.Password) < 8 {
		http.Error(w, "password must be at least 8 characters", http.StatusBadRequest)
		return
	}
	count, err := s.store.CountUsers(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	role := req.Role
	if role == "" {
		role = store.RoleUser
	}
	if count == 0 {
		role = store.RoleAdmin
	} else {
		admin, ok := userFromContext(r.Context())
		if !ok || admin.Role != store.RoleAdmin {
			http.Error(w, "admin required", http.StatusForbidden)
			return
		}
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		http.Error(w, "hash password failed", http.StatusInternalServerError)
		return
	}
	newUser := store.User{Username: req.Username, PasswordHash: string(hash), Role: role}
	var created store.User
	if count == 0 {
		created, err = s.store.CreateInitialAdmin(r.Context(), newUser)
	} else {
		created, err = s.store.CreateUser(r.Context(), newUser)
	}
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, store.ErrInitialAdminExists) {
			status = http.StatusConflict
		}
		http.Error(w, err.Error(), status)
		return
	}
	if count == 0 {
		_ = s.setSessionCookie(w, created)
	}
	writeJSON(w, map[string]any{"ok": true, "user": userToPublic(created)})
}

func (s *Server) requireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.userFromRequest(r)
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), userContextKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) requireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := s.userFromRequest(r); ok {
			if u.Role != store.RoleAdmin {
				http.Error(w, "admin required", http.StatusForbidden)
				return
			}
			ctx := context.WithValue(r.Context(), userContextKey, u)
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}

		token := extractBearer(r)
		if token == "" {
			token = r.URL.Query().Get("admin_token")
		}
		snap := s.cfg.Snapshot()
		if token == "" || snap.AdminToken == "" || token != snap.AdminToken {
			http.Error(w, "admin token required", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) attachOptionalUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, ok := s.userFromRequest(r); ok {
			r = r.WithContext(context.WithValue(r.Context(), userContextKey, u))
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireSameOriginForMutating(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		ct := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
		if ct != "application/json" {
			http.Error(w, "application/json content-type required", http.StatusUnsupportedMediaType)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			if !sameOrigin(r, origin) {
				http.Error(w, "origin rejected", http.StatusForbidden)
				return
			}
		} else if referer := r.Header.Get("Referer"); referer != "" {
			if !sameOrigin(r, referer) {
				http.Error(w, "referer rejected", http.StatusForbidden)
				return
			}
		} else if cookie, err := r.Cookie(sessionCookieName); err == nil && cookie.Value != "" {
			http.Error(w, "origin or referer required", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func sameOrigin(r *http.Request, value string) bool {
	if value == "" {
		return true
	}
	host := r.Host
	if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
		host = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if forwarded := r.Header.Get("X-Forwarded-Proto"); forwarded != "" {
		scheme = strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	prefix := scheme + "://" + host
	return value == prefix || strings.HasPrefix(value, prefix+"/")
}

func (s *Server) userFromRequest(r *http.Request) (store.User, bool) {
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return store.User{}, false
	}
	claims, err := s.verifySessionToken(cookie.Value)
	if err != nil {
		return store.User{}, false
	}
	u, err := s.store.FindUserByID(r.Context(), claims.Subject)
	if err != nil || u.Disabled || u.Username != claims.Username || u.Role != claims.Role {
		return store.User{}, false
	}
	return u, true
}

func (s *Server) setSessionCookie(w http.ResponseWriter, u store.User) error {
	snap := s.cfg.Snapshot()
	ttl := snap.AuthSessionDuration()
	now := time.Now()
	claims := sessionClaims{Subject: u.ID, Username: u.Username, Role: u.Role, IssuedAt: now.Unix(), Expires: now.Add(ttl).Unix()}
	token, err := s.signSessionToken(claims)
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: token, Path: "/", HttpOnly: true, Secure: snap.AuthCookieSecure, SameSite: http.SameSiteLaxMode, Expires: now.Add(ttl), MaxAge: int(ttl.Seconds())})
	return nil
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	snap := s.cfg.Snapshot()
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, Secure: snap.AuthCookieSecure, SameSite: http.SameSiteLaxMode, Expires: time.Unix(0, 0), MaxAge: -1})
}

func (s *Server) signSessionToken(claims sessionClaims) (string, error) {
	secret, err := s.sessionSecret()
	if err != nil {
		return "", err
	}
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	h, _ := json.Marshal(header)
	p, _ := json.Marshal(claims)
	unsigned := base64.RawURLEncoding.EncodeToString(h) + "." + base64.RawURLEncoding.EncodeToString(p)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

func (s *Server) verifySessionToken(token string) (sessionClaims, error) {
	var claims sessionClaims
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return claims, fmt.Errorf("invalid token")
	}
	secret, err := s.sessionSecret()
	if err != nil {
		return claims, err
	}
	unsigned := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(unsigned))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || !hmac.Equal(got, want) {
		return claims, fmt.Errorf("invalid signature")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, err
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return claims, err
	}
	if claims.Subject <= 0 || claims.Expires <= time.Now().Unix() {
		return claims, fmt.Errorf("expired token")
	}
	return claims, nil
}

func (s *Server) sessionSecret() (string, error) {
	snap := s.cfg.Snapshot()
	secret := strings.TrimSpace(snap.AuthJWTSecret)
	if secret == "" {
		secret = strings.TrimSpace(snap.AdminToken)
	}
	if secret != "" && secret != "change-me-admin" {
		return secret, nil
	}
	if s.ephemeralSessionSecret == "" {
		return "", fmt.Errorf("auth_jwt_secret is required")
	}
	return s.ephemeralSessionSecret, nil
}

func newEphemeralSessionSecret() string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Errorf("generate session secret: %w", err))
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
