package httpapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	docmosthttp "centipede/internal/modules/docmost/adapter/in/http"
	docmostpostgres "centipede/internal/modules/docmost/adapter/out/postgres"
	docmostsmtp "centipede/internal/modules/docmost/adapter/out/smtp"
	docmoststorage "centipede/internal/modules/docmost/adapter/out/storage"
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
	allowedOrigins := append([]string{}, cfg.Server.CORSOrigins...)
	if cfg.Migration.FrontendBaseURL != "" {
		allowedOrigins = append(allowedOrigins, cfg.Migration.FrontendBaseURL)
	}
	// The frontend URL is needed for credentialed REST CORS and invitation
	// links, but it should not force the WebSocket server into a static-origin
	// mode. Empty websocket origins make ygo perform a same-origin check, which
	// supports Docker access through localhost, LAN IPs and reverse proxies.
	websocketOrigins := append([]string{}, cfg.Server.CORSOrigins...)
	engine.Use(gin.Recovery(), requestID(), requestLogger(logger), securityHeaders(), cors(allowedOrigins))
	engine.GET("/", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello World!")
	})
	engine.GET("/robots.txt", func(c *gin.Context) {
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte("User-Agent: *\nDisallow: /login\nDisallow: /forgot-password\n"))
	})
	engine.GET("/api", func(c *gin.Context) {
		c.String(http.StatusOK, "Hello World!")
	})

	systemhttp.Register(engine, database, cfg.Environment)
	attachmentStorage, err := docmoststorage.NewLocal(cfg.Storage.DataDir)
	if err != nil {
		panic(err)
	}
	docmostRepository := docmostpostgres.New(database)
	docmostHandler := docmosthttp.NewHandler(
		docmostRepository,
		cfg.Auth.JWTSecret,
		cfg.Auth.RefreshTokenTTL,
		cfg.Auth.CookieSecure,
		cfg.Migration.FrontendBaseURL,
		cfg.Server.PublicURL,
		cfg.Migration.LegacyBaseURL,
		docmostsmtp.New(cfg.Mail),
		attachmentStorage,
		cfg.Storage.MaxUploadBytes,
		cfg.AI,
		cfg.PDFOCR,
	)
	docmostHandler.SetLicenseSigningSecret(cfg.License.SigningSecret)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		docmostHandler.ResumePendingZipImports(ctx)
	}()
	collaborationHandler := docmosthttp.NewCollaborationHandler(docmostRepository, cfg.Auth.JWTSecret, websocketOrigins, cfg.Collaboration)
	engine.Any("/collab/:room", gin.WrapH(collaborationHandler))
	engine.GET("/api/collab/stats", func(c *gin.Context) {
		connections, documents := collaborationHandler.Stats()
		c.JSON(http.StatusOK, gin.H{"connections": connections, "documents": documents})
	})
	realtimeHandler := docmosthttp.NewRealtimeHandler(docmostRepository, cfg.Auth.JWTSecret, websocketOrigins)
	docmostHandler.SetRealtimeHandler(realtimeHandler)
	collaborationHandler.SetRealtimeHandler(realtimeHandler)
	collaborationHandler.SetNotificationEmailEnqueuer(docmostHandler.EnqueueNotificationEmail)
	collaborationHandler.SetAIEmbeddingEnqueuer(docmostHandler.EnqueueAIPageEmbedding)
	engine.GET("/realtime", gin.WrapH(realtimeHandler))
	docmostHandler.Register(engine)
	if cfg.Migration.LegacyBaseURL != "" {
		legacyProxy, err := newLegacyProxy(cfg.Migration.LegacyBaseURL)
		if err != nil {
			panic(err)
		}
		engine.NoRoute(gin.WrapH(legacyProxy))
	}

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
		// Match Docmost's frame policy: public share pages and already
		// authorized attachment responses may be embedded by the editor or an
		// external knowledge-base page. The attachment handler also removes
		// this header defensively, but skipping it here covers share responses
		// before their handler runs.
		path := c.Request.URL.Path
		if !strings.HasPrefix(path, "/share/") && !strings.HasPrefix(path, "/api/files/") {
			c.Header("X-Frame-Options", "DENY")
		}
		c.Header("Referrer-Policy", "no-referrer")
		c.Next()
	}
}
