package config

import (
	"os"
	"strconv"
	"time"
)

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
}

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

type AuthConfig struct {
	JWTSecret          string
	JWTExpiration      time.Duration
	RefreshExpiration  time.Duration
	VoiceJWTSecret     string
	VoiceJWTExpiration time.Duration
	OAuth              map[string]OAuthProvider
}

type OAuthProvider struct {
	ClientID     string
	ClientSecret string
	RedirectURL  string
	AuthURL      string
	TokenURL     string
	UserInfoURL  string
}

type VoiceConfig struct {
	UDPHost      string
	UDPPortStart int
	UDPPortEnd   int
	UDPPortCount int
	ControlPort  int
	ServerID     string
	Region       string
	Secret       string
	RegistryURL  string
	PublicHost   string
}

type LoggingConfig struct {
	Level      string
	Format     string
	Output     string
	EnableFile bool
	FilePath   string
}

type RedisConfig struct {
	Host     string
	Port     int
	Password string
	DB       int
	Enabled  bool
}

type StorageConfig struct {
	Path string
	URL  string
}

type RateLimitConfig struct {
	Enabled           bool
	RequestsPerMinute int
	Burst             int
}

type EmailConfig struct {
	SMTPHost    string
	SMTPPort    int
	Username    string
	Password    string
	FromAddress string
	FromName    string
}

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
			RefreshExpiration:  getEnvDuration("REFRESH_EXPIRATION", 7*24*time.Hour),
			VoiceJWTSecret:     getEnv("VOICE_JWT_SECRET", "change-me-voice-secret"),
			VoiceJWTExpiration: getEnvDuration("VOICE_JWT_EXPIRATION", 5*time.Minute),
			OAuth:              loadOAuthProviders(),
		},
		Voice: VoiceConfig{
			UDPHost:      getEnv("VOICE_UDP_HOST", "0.0.0.0"),
			UDPPortStart: getEnvInt("VOICE_UDP_PORT_START", 50000),
			UDPPortEnd:   getEnvInt("VOICE_UDP_PORT_END", 52000),
			UDPPortCount: getEnvInt("VOICE_UDP_PORT_COUNT", 50),
			ControlPort:  getEnvInt("VOICE_CONTROL_PORT", 9091),
			ServerID:     getEnv("VOICE_SERVER_ID", ""),
			Region:       getEnv("VOICE_REGION", "ru-west"),
			Secret:       getEnv("VOICE_SECRET", "change-me-voice-server-secret"),
			RegistryURL:  getEnv("REGISTRY_URL", "localhost:9090"),
			PublicHost:   getEnv("VOICE_PUBLIC_HOST", "localhost"),
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
		},
		Storage: StorageConfig{
			Path: getEnv("STORAGE_PATH", "./uploads"),
			URL:  getEnv("STORAGE_URL", "http://localhost:8080/files"),
		},
		Email: EmailConfig{
			SMTPHost: getEnv("EMAIL_SMTP_HOST", "smtp.example.com"),
			SMTPPort: getEnvInt("EMAIL_SMTP_PORT", 587),
			Username: getEnv("EMAIL_USERNAME", ""),
			Password: getEnv("EMAIL_PASSWORD", ""),
		},
	}
	return cfg, nil
}

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

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if intVal, err := strconv.Atoi(value); err == nil {
			return intVal
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if value := os.Getenv(key); value != "" {
		if boolVal, err := strconv.ParseBool(value); err == nil {
			return boolVal
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if duration, err := time.ParseDuration(value); err == nil {
			return duration
		}
	}
	return fallback
}
