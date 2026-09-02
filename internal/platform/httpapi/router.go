package httpapi

import (
	"net/http"
	"time"

	identityhttp "centipede/internal/modules/identity/adapter/in/http"
	identityjwt "centipede/internal/modules/identity/adapter/out/jwt"
	identitypassword "centipede/internal/modules/identity/adapter/out/password"
	identitypostgres "centipede/internal/modules/identity/adapter/out/postgres"
	identityapplication "centipede/internal/modules/identity/application"
	systemhttp "centipede/internal/modules/system/adapter/in/http"
	vocabularyhttp "centipede/internal/modules/vocabulary/adapter/in/http"
	vocabularypostgres "centipede/internal/modules/vocabulary/adapter/out/postgres"
	vocabularyapplication "centipede/internal/modules/vocabulary/application"
	"centipede/internal/platform/config"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"
)

func NewRouter(cfg config.Config, database *pgxpool.Pool, logger *zap.Logger) http.Handler {
	if cfg.Environment == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	engine := gin.New()
	_ = engine.SetTrustedProxies(nil)
	engine.Use(gin.Recovery(), requestID(), requestLogger(logger), securityHeaders())

	systemhttp.Register(engine, database, cfg.Environment)

	identityRepository := identitypostgres.New(database)
	tokenService := identityjwt.New(cfg.Auth.JWTSecret, "centipede")
	authService := identityapplication.NewAuthService(identityRepository, identityRepository, identitypassword.Hasher{}, tokenService, cfg.Auth.AccessTokenTTL, cfg.Auth.RefreshTokenTTL)
	authHandler := identityhttp.NewHandler(authService, cfg.Auth.AccessTokenTTL, cfg.Auth.RefreshTokenTTL, cfg.Auth.RefreshCookie, cfg.Auth.CookieSecure)
	authHandler.Register(engine)

	vocabularyRepository := vocabularypostgres.New(database)
	vocabularyHandler := vocabularyhttp.NewHandler(vocabularyapplication.NewService(vocabularyRepository))
	vocabularyHandler.Register(engine, authHandler.Middleware())

	api := engine.Group("/api/v1")
	api.GET("/system/info", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"name":              "Centipede",
			"architecture":      "modular-monolith",
			"identity_provider": cfg.SSO.IssuerURL,
		})
	})

	return engine
}

func requestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = time.Now().UTC().Format("20060102T150405.000000000")
		}
		c.Set("request_id", requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

func requestLogger(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		startedAt := time.Now()
		c.Next()
		logger.Info("http request",
			zap.String("request_id", c.GetString("request_id")),
			zap.String("method", c.Request.Method),
			zap.String("path", c.FullPath()),
			zap.Int("status", c.Writer.Status()),
			zap.Int64("latency_ms", time.Since(startedAt).Milliseconds()),
		)
	}
}

func securityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	}
}
