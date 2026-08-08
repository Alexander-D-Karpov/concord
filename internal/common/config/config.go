package config

import (
	"os"
	"strconv"
	"time"
)

// Config is the fully assembled application configuration, grouping each
// subsystem's settings. It is populated by Load from environment variables.
type Config struct {
	Server    ServerConfig
	Database  DatabaseConfig
	Auth      AuthConfig
	Voice     VoiceConfig
	Logging   LoggingConfig
	Redis     RedisConfig
	RateLimit RateLimitConfig
	Storage   StorageConfig
	Email     EmailConfig
	Push      PushConfig
}

// ServerConfig holds HTTP/gRPC listener addresses, request timeouts, and
// optional TLS certificate paths.
type ServerConfig struct {
	Host         string
	Port         int
	GRPCPort     int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	TLSCertFile  string
	TLSKeyFile   string
}

// DatabaseConfig holds PostgreSQL connection parameters and pgx pool sizing and
// lifetime limits.
type DatabaseConfig struct {
	Host            string
	Port            int
	User            string
	Password        string
	Database        string
	MaxConns        int
	MinConns        int
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// AuthConfig holds JWT signing secrets and expirations for both user and voice
// tokens, plus the configured OAuth providers keyed by name.
type AuthConfig struct {
	JWTSecret          string
	JWTExpiration      time.Duration
	RefreshExpiration  time.Duration
	VoiceJWTSecret     string
	VoiceJWTExpiration time.Duration
	OAuth              map[string]OAuthProvider
	// LoginMaxAttempts is the number of failed password logins per identifier
	// (within LoginAttemptWindow) that trips a lockout; <= 0 disables lockout.
	LoginMaxAttempts int
	// LoginLockoutPeriod is how long an identifier stays locked once tripped.
	LoginLockoutPeriod time.Duration
	// LoginAttemptWindow is the sliding window over which failures are counted.
	LoginAttemptWindow time.Duration
}

// OAuthProvider holds the client credentials and endpoint URLs for a single
// OAuth2 identity provider (e.g. Google, GitHub).
type OAuthProvider struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
}

// VoiceConfig holds the voice server's UDP/TCP listener settings, region and
// registry identity, shared secrets, and public advertisement addresses.
type VoiceConfig struct {
	UDPHost        string
	UDPPortStart   int
	UDPPortEnd     int
	UDPPortCount   int
	ControlPort    int
	ServerID       string
	Region         string
	Secret         string
	RegisterSecret string
	RegistryURL    string
	PublicHost     string
	StatusPort     int
	JWTExpiration  time.Duration
	SinglePort     bool
	PublicUDPPort  int
	SocketCount    int
	TCPPort        int
	TLSCert        string
	TLSKey         string
	// Debug enables voice debug features: the concord-api voice-join RPCs skip the
	// room-membership check so the throughput/stress harness can fast-join. Off by
	// default; MUST stay off in production.
	Debug bool
}

// LoggingConfig selects the log level, encoding (json/console), output sink, and
// optional file logging.
type LoggingConfig struct {
	Level      string
	Format     string
	Output     string
	EnableFile bool
	FilePath   string
}

// RedisConfig holds Redis connection settings; Enabled gates whether Redis-backed
// features (cache, rate limiting) are wired up at all.
type RedisConfig struct {
	Host     string
	Port     int
	Password string
	DB       int
	Enabled  bool
}

// StorageConfig configures local file storage: Path is the on-disk upload
// directory and URL is the public path prefix files are served under.
type StorageConfig struct {
	Path string
	URL  string
}

// RateLimitConfig configures the per-client request rate limiter. BypassToken,
// when presented by a caller, exempts it from limiting.
type RateLimitConfig struct {
	Enabled           bool
	RequestsPerMinute int
	Burst             int
	BypassToken       string
}

// EmailConfig holds SMTP credentials and the default From identity for outgoing
// mail.
type EmailConfig struct {
	SMTPHost    string
	SMTPPort    int
	Username    string
	Password    string
	FromAddress string
	FromName    string
}

// PushConfig controls FCM push notifications. When Enabled is false, no push
// dispatch wiring is installed (device registration still works). CredentialsFile
// is the path to the Firebase service-account JSON used by the real FCM sender.
type PushConfig struct {
	Enabled         bool
	CredentialsFile string
}

// Load builds a Config by reading environment variables via os.Getenv, falling
// back to built-in defaults for any that are unset or unparseable. It never
// fails today (the error return is reserved for future validation). Note the
// default JWT and voice secrets are insecure placeholders that must be
// overridden in production.
func Load() (*Config, error) {
	cfg := &Config{
		Server: ServerConfig{
			Host:         getEnv("SERVER_HOST", "0.0.0.0"),
			Port:         getEnvInt("SERVER_PORT", 8080),
			GRPCPort:     getEnvInt("GRPC_PORT", 9090),
			ReadTimeout:  getEnvDuration("READ_TIMEOUT", 10*time.Second),
			WriteTimeout: getEnvDuration("WRITE_TIMEOUT", 10*time.Second),
			IdleTimeout:  getEnvDuration("IDLE_TIMEOUT", 120*time.Second),
			TLSCertFile:  getEnv("TLS_CERT_FILE", ""),
			TLSKeyFile:   getEnv("TLS_KEY_FILE", ""),
		},
		Database: DatabaseConfig{
			Host:            getEnv("DB_HOST", "localhost"),
			Port:            getEnvInt("DB_PORT", 5432),
			User:            getEnv("DB_USER", "postgres"),
			Password:        getEnv("DB_PASSWORD", "postgres"),
			Database:        getEnv("DB_NAME", "concord"),
			MaxConns:        getEnvInt("DB_MAX_CONNS", 25),
			MinConns:        getEnvInt("DB_MIN_CONNS", 5),
			MaxConnLifetime: getEnvDuration("DB_MAX_CONN_LIFETIME", 5*time.Minute),
			MaxConnIdleTime: getEnvDuration("DB_MAX_CONN_IDLE_TIME", 5*time.Minute),
		},
		Auth: AuthConfig{
			JWTSecret:          getEnv("JWT_SECRET", "change-me-in-production"),
			JWTExpiration:      getEnvDuration("JWT_EXPIRATION", 15*time.Minute),
			RefreshExpiration:  getEnvDuration("REFRESH_EXPIRATION", 30*24*time.Hour),
			VoiceJWTSecret:     getEnv("VOICE_JWT_SECRET", "change-me-voice-secret"),
			VoiceJWTExpiration: getEnvDuration("VOICE_JWT_EXPIRATION", 5*time.Minute),
			OAuth:              loadOAuthProviders(),
			LoginMaxAttempts:   getEnvInt("LOGIN_MAX_ATTEMPTS", 5),
			LoginLockoutPeriod: getEnvDuration("LOGIN_LOCKOUT_PERIOD", 15*time.Minute),
			LoginAttemptWindow: getEnvDuration("LOGIN_ATTEMPT_WINDOW", 15*time.Minute),
		},
		Voice: VoiceConfig{
			UDPHost:        getEnv("VOICE_UDP_HOST", "0.0.0.0"),
			UDPPortStart:   getEnvInt("VOICE_UDP_PORT_START", 50000),
			UDPPortEnd:     getEnvInt("VOICE_UDP_PORT_END", 52000),
			UDPPortCount:   getEnvInt("VOICE_UDP_PORT_COUNT", 50),
			ControlPort:    getEnvInt("VOICE_CONTROL_PORT", 9001),
			ServerID:       getEnv("VOICE_SERVER_ID", ""),
			Region:         getEnv("VOICE_REGION", "ru-west-1"),
			Secret:         getEnv("VOICE_SECRET", "change-me-voice-server-secret"),
			RegisterSecret: getEnv("VOICE_REGISTER_SECRET", ""),
			RegistryURL:    getEnv("REGISTRY_URL", "localhost:9000"),
			PublicHost:     getEnv("VOICE_PUBLIC_HOST", "localhost"),
			StatusPort:     getEnvInt("VOICE_STATUS_PORT", 9092),
			JWTExpiration:  getEnvDuration("VOICE_JWT_EXPIRATION", 5*time.Minute),
			SinglePort:     getEnvBool("VOICE_SINGLE_PORT", false),
			PublicUDPPort:  getEnvInt("VOICE_PUBLIC_UDP_PORT", 0),
			SocketCount:    getEnvInt("VOICE_SOCKET_COUNT", 0),
			TCPPort:        getEnvInt("VOICE_TCP_PORT", 0),
			TLSCert:        getEnv("VOICE_TLS_CERT", ""),
			TLSKey:         getEnv("VOICE_TLS_KEY", ""),
			Debug:          getEnvBool("VOICE_DEBUG", false),
		},
		Logging: LoggingConfig{
			Level:      getEnv("LOG_LEVEL", "info"),
			Format:     getEnv("LOG_FORMAT", "json"),
			Output:     getEnv("LOG_OUTPUT", "stdout"),
			EnableFile: getEnvBool("LOG_ENABLE_FILE", false),
			FilePath:   getEnv("LOG_FILE_PATH", "/var/log/concord/app.log"),
		},
		Redis: RedisConfig{
			Host:     getEnv("REDIS_HOST", "localhost"),
			Port:     getEnvInt("REDIS_PORT", 6379),
			Password: getEnv("REDIS_PASSWORD", ""),
			DB:       getEnvInt("REDIS_DB", 0),
			Enabled:  getEnvBool("REDIS_ENABLED", true),
		},
		RateLimit: RateLimitConfig{
			Enabled:           getEnvBool("RATE_LIMIT_ENABLED", true),
			RequestsPerMinute: getEnvInt("RATE_LIMIT_REQUESTS_PER_MINUTE", 60),
			Burst:             getEnvInt("RATE_LIMIT_BURST", 10),
			BypassToken:       getEnv("RATE_LIMIT_BYPASS_TOKEN", ""),
		},
		Storage: StorageConfig{
			Path: getEnv("STORAGE_PATH", "./uploads"),
			URL:  getEnv("STORAGE_URL", "/files"),
		},
		Email: EmailConfig{
			SMTPHost: getEnv("EMAIL_SMTP_HOST", "smtp.example.com"),
			SMTPPort: getEnvInt("EMAIL_SMTP_PORT", 587),
			Username: getEnv("EMAIL_USERNAME", ""),
			Password: getEnv("EMAIL_PASSWORD", ""),
		},
		Push: PushConfig{
			Enabled:         getEnvBool("PUSH_ENABLED", false),
			CredentialsFile: getEnv("PUSH_CREDENTIALS_FILE", ""),
		},
	}
	return cfg, nil
}

// loadOAuthProviders returns the OAuth providers whose client-ID env var is set,
// keyed by provider name ("google", "github"). Providers without a configured
// client ID are omitted, so an empty map means OAuth is effectively disabled.
func loadOAuthProviders() map[string]OAuthProvider {
	providers := make(map[string]OAuthProvider)

	if clientID := getEnv("OAUTH_GOOGLE_CLIENT_ID", ""); clientID != "" {
		providers["google"] = OAuthProvider{
			ClientID:     clientID,
			ClientSecret: getEnv("OAUTH_GOOGLE_CLIENT_SECRET", ""),
			RedirectURL:  getEnv("OAUTH_GOOGLE_REDIRECT_URL", ""),
			AuthURL:      "https://accounts.google.com/o/oauth2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			UserInfoURL:  "https://www.googleapis.com/oauth2/v2/userinfo",
		}
	}

	if clientID := getEnv("OAUTH_GITHUB_CLIENT_ID", ""); clientID != "" {
		providers["github"] = OAuthProvider{
			ClientID:     clientID,
			ClientSecret: getEnv("OAUTH_GITHUB_CLIENT_SECRET", ""),
			RedirectURL:  getEnv("OAUTH_GITHUB_REDIRECT_URL", ""),
			AuthURL:      "https://github.com/login/oauth/authorize",
			TokenURL:     "https://github.com/login/oauth/access_token",
			UserInfoURL:  "https://api.github.com/user",
		}
	}

	return providers
}

// getEnv returns the value of environment variable key, or fallback if it is
// unset or empty.
func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// getEnvInt returns environment variable key parsed as an int, or fallback if it
// is unset, empty, or not a valid integer.
func getEnvInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return fallback
}

// getEnvBool returns environment variable key parsed by strconv.ParseBool (so
// "1", "true", "t", etc. are accepted), or fallback if it is unset, empty, or
// unparseable.
func getEnvBool(key string, fallback bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolVal, err := strconv.ParseBool(value); err == nil {
			return boolVal
		}
	}
	return fallback
}

// getEnvDuration returns environment variable key parsed by time.ParseDuration
// (e.g. "10s", "5m"), or fallback if it is unset, empty, or unparseable.
func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return fallback
}
